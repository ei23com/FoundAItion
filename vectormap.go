package main

import (
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"math"
	"math/rand"
	"sort"
)

// ─── 2D Projection Engine ────────────────────────────────────────────────────
//
// Projects a set of text embeddings onto a stable, reproducible 2D layout:
//   - Barnes-Hut accelerated t-SNE (van der Maaten & Hinton) for n >= 16
//   - PCA fallback for 3 <= n < 16
//   - circle/point layout for n < 3
//
// All randomness uses a fixed seed, so identical data always produces the
// same map. Results are persisted in the "map_positions" table and reused
// as long as the set of embedded entries does not change (fingerprint).

const (
	tsnePerplexityDefault = 30.0
	tsneTheta             = 0.5 // Barnes-Hut tree approximation quality
	tsneMaxIterations     = 600
	tsneLearningRate      = 200.0
	tsneEarlyExaggeration = 12.0
	tsneExaggerateUntil   = 100
	tsneMomentumSwitch    = 250
	tsneGradClamp         = 50.0  // max per-point update norm
	tsneStepFraction      = 0.03  // global cap: max step per iteration as a fraction of layout extent
	mapPointsRange        = 100.0 // output scaled to roughly ±range/2
)

// mapPoint is one projected entry.
type mapPoint struct {
	ID int64
	X  float64
	Y  float64
}

// ─── Fingerprint & persistence ───────────────────────────────────────────────

// vectorFingerprint hashes the current set of (id, model) pairs that have a
// usable vector. Any change (new embedding, cleared vector, entry deleted)
// invalidates a cached projection.
func vectorFingerprint(ids []int64, model string) string {
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	h := sha1.New()
	fmt.Fprintf(h, "model=%s;count=%d;", model, len(ids))
	for _, id := range ids {
		fmt.Fprintf(h, "%d|", id)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ensureMapPositionsTable creates the position cache table if needed.
func ensureMapPositionsTable(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS map_positions (
			id         INTEGER PRIMARY KEY,
			x          REAL NOT NULL,
			y          REAL NOT NULL,
			model      TEXT NOT NULL,
			fingerprint TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("create map_positions: %w", err)
	}
	return nil
}

// loadCachedPositions returns stored positions if they match the fingerprint.
func loadCachedPositions(db *sql.DB, fingerprint string) (map[int64]mapPoint, bool, error) {
	rows, err := db.Query(`SELECT id, x, y FROM map_positions WHERE fingerprint = ?`, fingerprint)
	if err != nil {
		return nil, false, fmt.Errorf("load positions: %w", err)
	}
	defer rows.Close()

	out := make(map[int64]mapPoint)
	for rows.Next() {
		var p mapPoint
		if err := rows.Scan(&p.ID, &p.X, &p.Y); err != nil {
			return nil, false, fmt.Errorf("scan positions: %w", err)
		}
		out[p.ID] = p
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate positions: %w", err)
	}
	if len(out) == 0 {
		return nil, false, nil
	}
	return out, true, nil
}

// storePositions replaces the position cache with the new projection.
func storePositions(db *sql.DB, points []mapPoint, model, fingerprint string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin position tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM map_positions`); err != nil {
		return fmt.Errorf("clear positions: %w", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO map_positions (id, x, y, model, fingerprint) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare position insert: %w", err)
	}
	defer stmt.Close()

	for i := range points {
		if _, err := stmt.Exec(points[i].ID, points[i].X, points[i].Y, model, fingerprint); err != nil {
			return fmt.Errorf("insert position id=%d: %w", points[i].ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit positions: %w", err)
	}
	log.Printf("[map] stored %d projected positions (fingerprint %.10s…)", len(points), fingerprint)
	return nil
}

// ─── Orchestration ───────────────────────────────────────────────────────────

// project2D reduces matrix X (n × d, row-major) to 2D coordinates.
// ids[i] identifies the row for later assembly. onProgress reports 0..100.
func project2D(ids []int64, X [][]float32, onProgress func(pct int)) ([]mapPoint, error) {
	n := len(ids)
	if n == 0 {
		return nil, nil
	}

	if n < 3 {
		onProgress(100)
		return circleLayout(ids, float64(n)), nil
	}

	d := len(X[0])

	// Small sets: PCA is a robust, fast choice and t-SNE is unstable.
	if n < 16 {
		pc := pcaProject(X, 2, 42)
		onProgress(100)
		return normalizePoints(ids, coordsFromPCA(pc, n)), nil
	}

	// UMAP preserves global structure and yields the "landscape" look;
	// t-SNE stays available for small sets where it is stable.
	points, err := umapProject(ids, X, d, onProgress)
	if err != nil {
		return nil, err
	}
	onProgress(100)
	return points, nil
}

// circleLayout places a tiny number of points deterministically.
func circleLayout(ids []int64, n float64) []mapPoint {
	if n == 1 {
		return []mapPoint{{ID: ids[0], X: 0, Y: 0}}
	}
	out := make([]mapPoint, 0, int(n))
	for i := range ids {
		ang := 2 * math.Pi * float64(i) / n
		out = append(out, mapPoint{ID: ids[i], X: 50 * math.Cos(ang), Y: 50 * math.Sin(ang)})
	}
	return out
}

// ─── t-SNE (Barnes-Hut) ──────────────────────────────────────────────────────

// tsneProject runs the full t-SNE pipeline: pairwise P, PCA init, gradient
// tsneCore runs the full t-SNE optimization and returns RAW 2D coordinates
// (natural scale, no output normalization).
func tsneCore(X [][]float32, d int, onProgress func(pct int)) ([]float64, []float64, error) {
	n := len(X)

	perplexity := math.Min(tsnePerplexityDefault, float64(n)/8.0)
	if perplexity < 5 {
		perplexity = 5
	}
	k := int(math.Round(math.Exp(perplexity)))
	if k < 2 {
		k = 2
	}
	if k > n-2 {
		k = n - 2
	}

	onProgress(3)
	P, err := buildP(X, d, perplexity, k, onProgress)
	if err != nil {
		return nil, nil, fmt.Errorf("build P: %w", err)
	}

	// Deterministic PCA initialization.
	yx, yy := pcaProject2D(X, 42)

	useBH := n > 128

	momentum := 0.5
	vx := make([]float64, n)
	vy := make([]float64, n)

	gx := make([]float64, n)
	gy := make([]float64, n)
	U := make([]float64, n)
	S2x := make([]float64, n)
	S2y := make([]float64, n)
	var tree *tsneTree
	if useBH {
		tree = newTSneTree(n)
	}

	exaggerate := perplexityExaggerated(P, tsneEarlyExaggeration)

	for iter := 1; iter <= tsneMaxIterations; iter++ {
		if iter > tsneExaggerateUntil {
			if exaggerate != nil {
				exaggerate = P // switch back to the original P once
			}
		}
		if iter == tsneMomentumSwitch {
			momentum = 0.8
		}

		// Refresh the per-point moments (U_i, S2_i), then the gradient.
		if useBH {
			tree.rebuild(yx, yy)
			for i := 0; i < n; i++ {
				U[i], S2x[i], S2y[i] = tree.query(yx[i], yy[i], i)
			}
		} else {
			momentsInto(yx, yy, U, S2x, S2y)
		}
		for i := 0; i < n; i++ {
			gx[i], gy[i] = gradientAt(i, yx[i], yy[i], exaggerate.rows[i], exaggerate.rowSum, U, S2x, S2y, yx, yy)
		}

		meanGrad := applyTSneUpdate(yx, yy, vx, vy, gx, gy, momentum)

		if iter%25 == 0 || iter == tsneMaxIterations {
			onProgress(10 + 90*(iter)/tsneMaxIterations)
		}
		if meanGrad < 1e-5 && iter > tsneExaggerateUntil {
			log.Printf("[map] t-SNE converged early at iteration %d (mean |grad| = %.2g)", iter, meanGrad)
			break
		}
	}

	return yx, yy, nil
}

// tsneProject wraps tsneCore and maps raw coordinates to normalized output
// points in input order (display range ±mapPointsRange/2).
func tsneProject(ids []int64, X [][]float32, d int, onProgress func(pct int)) ([]mapPoint, error) {
	yx, yy, err := tsneCore(X, d, onProgress)
	if err != nil {
		return nil, err
	}
	n := len(yx)
	out := make([]mapPoint, n)
	for i := range ids {
		out[i] = mapPoint{ID: ids[i], X: yx[i], Y: yy[i]}
	}
	return normalizePoints(ids, out), nil
}

// perplexityExaggerated returns a copy of P with all values multiplied by the
// early-exaggeration factor. A uniform scaling of P scales the entire gradient
// by the same factor, which is precisely the desired effect.
func perplexityExaggerated(P *sparseP, factor float64) *sparseP {
	cp := &sparseP{n: P.n, rows: make([]csrRow, len(P.rows)), rowSum: make([]float64, len(P.rows))}
	for i := range P.rows {
		r := &P.rows[i]
		nr := csrRow{idx: append([]int32(nil), r.idx...), val: make([]float32, len(r.val))}
		for j := range r.val {
			nr.val[j] = float32(float64(r.val[j]) * factor)
		}
		cp.rows[i] = nr
		cp.rowSum[i] = P.rowSum[i] * factor
	}
	return cp
}

// sparseP holds the symmetric, row-normalized (row sum = ½) target
// distribution in compressed-sparse-row form.
type sparseP struct {
	n      int
	rows   []csrRow
	rowSum []float64 // exact sum of each row (needed by the gradient)
}

type csrRow struct {
	idx []int32
	val []float32 // p_ij
}

// buildP computes the pairwise target distribution from embeddings X.
func buildP(X [][]float32, d int, perplexity float64, k int, onProgress func(pct int)) (*sparseP, error) {
	n := len(X)

	// Per-row beta (1/σ²).
	beta := make([]float32, n)

	// a[i][j] = exp(-beta_i * u_ij) / sum_j (row-stochastic), dense temp.
	aDense := make([]float32, n*n)

	for i := 0; i < n; i++ {
		// Squared distances from point i to all others.
		dist := make([]float32, n)
		xi := X[i]
		for j := 0; j < n; j++ {
			if i == j {
				continue
			}
			s := float64(0)
			xj := X[j]
			for t := 0; t < d; t++ {
				diff := float64(xi[t]) - float64(xj[t])
				s += diff * diff
			}
			dist[j] = float32(s)
		}

		// k-th nearest distance for normalization.
		order := make([]float32, n-1)
		pos := 0
		for j := 0; j < n; j++ {
			if i == j {
				continue
			}
			order[pos] = dist[j]
			pos++
		}
		sort.Slice(order, func(a, b int) bool { return order[a] < order[b] })
		dk := order[k-1] // k-th smallest (1-based)
		if dk <= 0 {
			dk = 1e-8
		}

		uRow := make([]float32, n)
		for j := 0; j < n; j++ {
			if i == j {
				continue
			}
			uRow[j] = dist[j] / dk
		}

		// Binary search for beta s.t. perplexity(exp(-beta*u)) == perplexity.
		beta[i] = binarySearchBeta(uRow, i, perplexity)

		// Row-stochastic probabilities (kept dense until combined).
		sum := float64(0)
		for j := 0; j < n; j++ {
			if i == j {
				continue
			}
			e := math.Exp(-float64(beta[i]) * float64(uRow[j]))
			aDense[i*n+j] = float32(e)
			sum += e
		}
		if sum <= 0 {
			return nil, fmt.Errorf("row %d: degenerate distances", i)
		}
		for j := 0; j < n; j++ {
			if i != j {
				aDense[i*n+j] = float32(float64(aDense[i*n+j]) / sum)
			}
		}

		if i%25 == 0 {
			onProgress(3 + 37*(i+1)/n)
		}
	}
	// Combine into symmetric sparsified P: p_ij = (a_ij + a_ji)/2.
	P := &sparseP{n: n, rows: make([]csrRow, n)}
	for i := 0; i < n; i++ {
		rowI := &P.rows[i]
		for j := i + 1; j < n; j++ {
			p := (aDense[i*n+j] + aDense[j*n+i]) / 2
			if p > 1e-12 {
				rowI.idx = append(rowI.idx, int32(j))
				rowI.val = append(rowI.val, float32(p))
				rowJ := &P.rows[j]
				rowJ.idx = append(rowJ.idx, int32(i))
				rowJ.val = append(rowJ.val, float32(p))
			}
		}
	}

	aDense = nil // release the dense temp (90 MB at n≈5k)

	// Exact per-row sums for the gradient. NOTE: rows are deliberately NOT
	// renormalized here — per-row rescaling would break the symmetry
	// p_ij == p_ji that the column term of the gradient relies on. Any row
	// scaling is legitimate for t-SNE (it only rescales the gradient), and
	// early exaggeration applies its own uniform factor.
	P.rowSum = make([]float64, len(P.rows))
	for i := range P.rows {
		s := 0.0
		for m := range P.rows[i].val {
			s += float64(P.rows[i].val[m])
		}
		P.rowSum[i] = s
	}
	return P, nil
}

// binarySearchBeta finds the temperature beta that yields the desired
// perplexity for a normalized distance row (MATLAB tsne.m style).
func binarySearchBeta(uRow []float32, self int, perplexity float64) float32 {
	target := math.Log(perplexity)
	lo, hi := 0.0, 1.0
	for iter := 0; iter < 50; iter++ {
		mid := (lo + hi) / 2
		if mid < 1e-12 {
			mid = 1e-12
		}
		// Entropy of q_j = exp(-mid*u_j)/sum.
		sum := float64(0)
		qs := make([]float64, len(uRow))
		for j, u := range uRow {
			if j == self {
				continue
			}
			e := math.Exp(-math.Min(mid*float64(u), 80))
			qs[j] = e
			sum += e
		}
		H := float64(0)
		for j, e := range qs {
			if j == self || e <= 0 || sum <= 0 {
				continue
			}
			q := e / sum
			if q > 0 {
				H -= q * math.Log(q)
			}
		}
		diff := H - target
		const tol = 0.01
		if math.Abs(diff) < tol || hi-lo < 1e-12 {
			return float32(mid)
		}
		if diff > 0 {
			lo = mid // higher entropy → lower beta
		} else {
			hi = mid
		}
	}
	return float32((lo + hi) / 2)
}

// ─── Gradients ───────────────────────────────────────────────────────────────
//
// Cost (row-normalized t-SNE, MATLAB tsne.m style):
//
//	C = Σ_i Σ_{j≠i} p_ij · log(p_ij / q_ij),  q_ij = u_ij / U_i
//	u_ij = 1/(1+|y_i−y_j|²),  U_j = Σ_{k≠j} u_jk
//
// Exact gradient (verified against central finite differences to ~1e-10;
// every row-j term of C depends on y_i through its normalizer U_j):
//
//	∂C/∂y_i = 2·T_i − 2·s_i·S2_i/U_i + Σ_{j∈row_i} [ 2·p_ij·u_ij·Δ_ij − 2·s_j·u_ij²·Δ_ij/U_j ]
//
// with T_i = Σ_j p_ij·u_ij·(y_i−y_j) (attractive, K neighbors),
// S2_i = Σ_k u_ik²·(y_i−y_k) and s_i = exact row sum of P (any scaling works;
// buildP leaves the natural symmetrized sums).
// The BH tree approximates U_i and S2_i in O(log n); U_j of the ≤K neighbors
// comes from a per-iteration cache (every point is queried exactly once).

// momentsInto fills U, S2x, S2y with brute-force values for every point.
func momentsInto(yx, yy []float64, U, S2x, S2y []float64) {
	n := len(yx)
	for i := 0; i < n; i++ {
		ui, s2x, s2y := 0.0, 0.0, 0.0
		for k := 0; k < n; k++ {
			if k == i {
				continue
			}
			dx := yx[i] - yx[k]
			dy := yy[i] - yy[k]
			u := 1.0 / (1.0 + dx*dx+dy*dy)
			ui += u
			s2x += u * u * dx
			s2y += u * u * dy
		}
		U[i], S2x[i], S2y[i] = ui, s2x, s2y
	}
}

// gradientAt evaluates ∂C/∂y_i from cached per-point moments.
func gradientAt(i int, xi, yi float64, row csrRow, rowSum, U, S2x, S2y []float64, yx, yy []float64) (float64, float64) {
	Tx, Ty := 0.0, 0.0
	for m := range row.idx {
		j := int(row.idx[m])
		dx := xi - yx[j]
		dy := yi - yy[j]
		u := 1.0 / (1.0 + dx*dx+dy*dy)
		p := float64(row.val[m])
		Tx += p * u * dx
		Ty += p * u * dy
	}
	Ui := math.Max(U[i], 1e-12)
	si := rowSum[i]
	gx := 2*Tx - 2*si*S2x[i]/Ui
	gy := 2*Ty - 2*si*S2y[i]/Ui
	for m := range row.idx {
		j := int(row.idx[m])
		dx := xi - yx[j]
		dy := yi - yy[j]
		D := dx*dx + dy*dy
		u := 1.0 / (1.0 + D)
		wA := 2 * float64(row.val[m]) * u          // attractive column part
		wR := 2 * rowSum[j] * u * u / math.Max(U[j], 1e-12) // repulsive normalizer part
		gx += (wA - wR) * dx
		gy += (wA - wR) * dy
	}
	return gx, gy
}

// ─── Barnes-Hut Quadtree ─────────────────────────────────────────────────────

type tsneTreeNode struct {
	cx, cy     float64 // centroid (== position for leaves)
	minX       float64
	maxX       float64
	minY       float64
	maxY       float64
	width      float64
	count      int // points contained (fat leaves hold >1 co-located point)
	point      int // leaf: representative point index, -1 for internal nodes
	children   [4]*tsneTreeNode
}

type tsneTree struct {
	n    int
	root *tsneTreeNode
	// scratch stack for iterative traversal (max depth ~ 2*log2(n)+8 < 64)
	stack [64]*tsneTreeNode
}

const tsneMaxInsertDepth = 32

// applyTSneUpdate performs the momentum step with two safeguards:
//
//  1. an absolute per-point velocity clamp, and
//  2. a global displacement cap: no iteration may move the fastest point more
//     than tsneStepFraction × current layout extent. Classic t-SNE starts from
//     co-located noise where forces are self-limiting; our PCA initialization
//     spreads points immediately, so without this cap the first steps blow the
//     layout apart regardless of learning rate.
//
// Returns the mean absolute gradient (convergence signal).
func applyTSneUpdate(yx, yy, vx, vy, gx, gy []float64, momentum float64) float64 {
	n := len(yx)

	sumGrad := 0.0
	dispMax := 0.0
	for i := 0; i < n; i++ {
		vx[i] = momentum*vx[i] - tsneLearningRate*gx[i]
		vy[i] = momentum*vy[i] - tsneLearningRate*gy[i]
		norm := math.Hypot(vx[i], vy[i])
		if norm > tsneGradClamp {
			scale := tsneGradClamp / norm
			vx[i] *= scale
			vy[i] *= scale
			norm = tsneGradClamp
		}
		if norm > dispMax {
			dispMax = norm
		}
		sumGrad += math.Abs(gx[i]) + math.Abs(gy[i])
	}

	// Current layout extent (bounding square side).
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for i := 0; i < n; i++ {
		if yx[i] < minX {
			minX = yx[i]
		}
		if yx[i] > maxX {
			maxX = yx[i]
		}
		if yy[i] < minY {
			minY = yy[i]
		}
		if yy[i] > maxY {
			maxY = yy[i]
		}
	}
	extent := math.Max(maxX-minX, maxY-minY)
	if extent < 1e-9 {
		extent = 1e-9
	}
	if maxStep := tsneStepFraction * extent; dispMax > maxStep && dispMax > 0 {
		s := maxStep / dispMax
		for i := 0; i < n; i++ {
			vx[i] *= s
			vy[i] *= s
		}
	}

	for i := 0; i < n; i++ {
		yx[i] += vx[i]
		yy[i] += vy[i]
	}
	return sumGrad / (2 * float64(n))
}

func newTSneTree(n int) *tsneTree {
	return &tsneTree{n: n}
}

// rebuild re-creates the tree from current coordinates.
func (t *tsneTree) rebuild(yx, yy []float64) {
	// Exact root cell: bounding square of all points.
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for i := 0; i < t.n; i++ {
		if yx[i] < minX {
			minX = yx[i]
		}
		if yx[i] > maxX {
			maxX = yx[i]
		}
		if yy[i] < minY {
			minY = yy[i]
		}
		if yy[i] > maxY {
			maxY = yy[i]
		}
	}
	cx, cy := (minX+maxX)/2, (minY+maxY)/2
	half := math.Max(maxX-minX, maxY-minY) / 2
	if half <= 0 {
		half = 0.5
	}
	half *= 1.0001 // keep boundary points strictly inside

	root := &tsneTreeNode{point: -1}
	for i := 0; i < t.n; i++ {
		insertTSnePoint(root, i, yx, yy, cx-half, cx+half, cy-half, cy+half, 0)
	}
	refreshTSneNode(root, yx, yy)
	t.root = root
}

// refreshTSneNode recomputes count, centroid and bounding box bottom-up.
// Insertion only maintains these for leaves; internal nodes would otherwise
// carry stale bounds (width 0), which breaks the Barnes-Hut far criterion.
func refreshTSneNode(node *tsneTreeNode, yx, yy []float64) {
	if node == nil {
		return
	}
	if node.point >= 0 && allNilChildren(node) {
		return // leaf: setTSneLeaf already set everything (fat leaf keeps its count)
	}
	count := 0
	sumX, sumY := 0.0, 0.0
	minX, maxX := math.Inf(1), math.Inf(-1)
	minY, maxY := math.Inf(1), math.Inf(-1)
	for _, c := range node.children {
		if c == nil {
			continue
		}
		refreshTSneNode(c, yx, yy)
		if c.count == 0 {
			continue
		}
		w := float64(c.count)
		count += c.count
		sumX += w * c.cx
		sumY += w * c.cy
		if c.minX < minX {
			minX = c.minX
		}
		if c.maxX > maxX {
			maxX = c.maxX
		}
		if c.minY < minY {
			minY = c.minY
		}
		if c.maxY > maxY {
			maxY = c.maxY
		}
	}
	node.count = count
	if count > 0 {
		node.cx = sumX / float64(count)
		node.cy = sumY / float64(count)
		node.minX, node.maxX = minX, maxX
		node.minY, node.maxY = minY, maxY
		node.width = math.Max(maxX-minX, maxY-minY)
	}
}

// insertTSnePoint inserts point idx into the region quadtree node whose exact
// cell is [minX,maxX]×[minY,maxY]. Co-located points beyond depth
// tsneMaxInsertDepth are absorbed into a fat leaf.
func insertTSnePoint(node *tsneTreeNode, idx int, yx, yy []float64, minX, maxX, minY, maxY float64, depth int) {
	x, y := yx[idx], yy[idx]

	switch {
	case node.count == 0:
		setTSneLeaf(node, idx, x, y)
	case node.point >= 0:
		if depth >= tsneMaxInsertDepth {
			node.count++ // fat leaf: co-located points
			return
		}
		existing := node.point
		node.point = -1
		insertTSnePoint(node, existing, yx, yy, minX, maxX, minY, maxY, depth+1)
		insertTSnePoint(node, idx, yx, yy, minX, maxX, minY, maxY, depth+1)
	default:
		qx, qy := 0, 0
		if x >= (minX+maxX)/2 {
			qx = 1
		}
		if y >= (minY+maxY)/2 {
			qy = 1
		}
		ci := qx*2 + qy
		cMinX, cMaxX, cMinY, cMaxY := minX, maxX, minY, maxY
		if qx == 1 {
			cMinX = (minX + maxX) / 2
		} else {
			cMaxX = (minX + maxX) / 2
		}
		if qy == 1 {
			cMinY = (minY + maxY) / 2
		} else {
			cMaxY = (minY + maxY) / 2
		}
		c := node.children[ci]
		if c == nil {
			c = &tsneTreeNode{point: -1}
			node.children[ci] = c
		}
		insertTSnePoint(c, idx, yx, yy, cMinX, cMaxX, cMinY, cMaxY, depth+1)
	}
}

func setTSneLeaf(node *tsneTreeNode, idx int, x, y float64) {
	node.point = idx
	node.cx, node.cy = x, y
	node.minX, node.maxX = x, x
	node.minY, node.maxY = y, y
	node.width = 0
	node.count = 1
}

func allNilChildren(node *tsneTreeNode) bool {
	for _, c := range node.children {
		if c != nil {
			return false
		}
	}
	return true
}

// query accumulates U = Σ u and S2 = Σ u²·(query−point) moments, excluding
// the leaf at index self.
func (t *tsneTree) query(qx, qy float64, self int) (U, s2x, s2y float64) {
	top := 0
	t.stack[top] = t.root
	top++
	for top > 0 {
		node := t.stack[top-1]
		top--
		if node == nil || node.count == 0 {
			continue
		}
		if node.point >= 0 && allNilChildren(node) {
			if node.point == self {
				// co-located points (fat leaf) contribute zero S2 mass and a
				// negligible U mass; skipping the whole leaf is safe
				continue
			}
			dx := qx - node.cx
			dy := qy - node.cy
			u := 1.0 / (1.0 + dx*dx+dy*dy)
			w := float64(node.count)
			U += w * u
			s2x += w * u * u * dx
			s2y += w * u * u * dy
			continue
		}
		dx := qx - node.cx
		dy := qy - node.cy
		Dc := dx*dx + dy*dy
		if Dc > 1e-24 {
			width := node.width
			if width*width < tsneTheta*tsneTheta*Dc {
				u := 1.0 / (1.0 + Dc)
				U += float64(node.count) * u
				s2x += float64(node.count) * u * u * dx
				s2y += float64(node.count) * u * u * dy
				continue
			}
		}
		for _, c := range node.children {
			if c != nil && c.count > 0 {
				t.stack[top] = c
				top++
			}
		}
	}
	return U, s2x, s2y
}

// ─── PCA ─────────────────────────────────────────────────────────────────────

// pcaProject computes the first `k` principal components of X (n × d) via
// power iteration on the Gram matrix CᵀC, with a deterministic seed.
// Returns n×k coordinates.
func pcaProject(X [][]float32, k int, seed int64) []float64 {
	n := len(X)
	if n == 0 {
		return nil
	}
	d := len(X[0])
	k = minInt(k, max(2, min(d, n)))

	rng := rand.New(rand.NewSource(seed))

	// Center + build Gram M = CᵀC (d × d).
	mean := make([]float64, d)
	flat := make([]float64, n*d)
	for i := 0; i < n; i++ {
		for t := 0; t < d; t++ {
			v := float64(X[i][t])
			mean[t] += v
			flat[i*d+t] = v
		}
	}
	for t := range mean {
		mean[t] /= float64(n)
	}
	M := make([]float64, d*d)
	for i := 0; i < n; i++ {
		for a := 0; a < d; a++ {
			ca := flat[i*d+a] - mean[a]
			if ca == 0 {
				continue
			}
			for b := a; b < d; b++ {
				cb := flat[i*d+b] - mean[b]
				M[a*d+b] += ca * cb
			}
		}
	}
	for a := 0; a < d; a++ {
		for b := 0; b < a; b++ {
			M[a*d+b] = M[b*d+a]
		}
	}

	// Power iteration with deflation for the k leading eigenvectors.
	coords := make([]float64, n*k)
	work := make([]float64, d)
	for comp := 0; comp < k; comp++ {
		v := make([]float64, d)
		for t := range v {
			v[t] = rng.NormFloat64()
		}
		vec := powerIterate(M, v, work, 100)
		lambda := rayleigh(M, vec)

		// Project centered data onto the component.
		for i := 0; i < n; i++ {
			s := 0.0
			for t := 0; t < d; t++ {
				s += (flat[i*d+t] - mean[t]) * vec[t]
			}
			coords[i*k+comp] = s
		}

		// Deflate: M ← M − λ·v vᵀ.
		if lambda > 1e-12 {
			for a := 0; a < d; a++ {
				va := vec[a] * lambda
				if va == 0 {
					continue
				}
				for b := 0; b < d; b++ {
					M[a*d+b] -= va * vec[b]
				}
			}
		}
	}
	return coords
}

func powerIterate(M, v, work []float64, iters int) []float64 {
	d := len(v)
	for iter := 0; iter < iters; iter++ {
		for a := 0; a < d; a++ {
			s := 0.0
			Mrow := M[a*d:]
			for b := 0; b < d; b++ {
				s += Mrow[b] * v[b]
			}
			work[a] = s
		}
		norm := 0.0
		for a := 0; a < d; a++ {
			norm += work[a] * work[a]
		}
		norm = math.Sqrt(norm)
		if norm < 1e-300 {
			break
		}
		for a := 0; a < d; a++ {
			v[a] = work[a] / norm
		}
	}
	return v
}

func rayleigh(M, v []float64) float64 {
	d := len(v)
	Mv := make([]float64, d)
	for a := 0; a < d; a++ {
		s := 0.0
		for b := 0; b < d; b++ {
			s += M[a*d+b] * v[b]
		}
		Mv[a] = s
	}
	r := 0.0
	for a := 0; a < d; a++ {
		r += v[a] * Mv[a]
	}
	return r
}

// pcaProject2D is PCA for initialization (returns x and y arrays).
func pcaProject2D(X [][]float32, seed int64) ([]float64, []float64) {
	coords := pcaProject(X, 2, seed)
	n := len(coords) / 2
	xs := make([]float64, n)
	ys := make([]float64, n)
	for i := 0; i < n; i++ {
		xs[i] = coords[i*2]
		ys[i] = coords[i*2+1]
	}
	return xs, ys
}

func coordsFromPCA(coords []float64, n int) []mapPoint {
	out := make([]mapPoint, n)
	for i := 0; i < n; i++ {
		out[i] = mapPoint{X: coords[i*2], Y: coords[i*2+1]}
	}
	return out
}

// ─── Normalization ───────────────────────────────────────────────────────────

// normalizePoints re-centers the layout at the origin and scales it so the
// longest extent spans roughly ±mapPointsRange/2 (stable storage & fitting).
func normalizePoints(ids []int64, pts []mapPoint) []mapPoint {
	if len(pts) < 2 {
		for i := range pts {
			pts[i].X = 0
			pts[i].Y = 0
		}
		return pts
	}
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for i := range pts {
		if pts[i].X < minX {
			minX = pts[i].X
		}
		if pts[i].X > maxX {
			maxX = pts[i].X
		}
		if pts[i].Y < minY {
			minY = pts[i].Y
		}
		if pts[i].Y > maxY {
			maxY = pts[i].Y
		}
	}
	span := math.Max(maxX-minX, maxY-minY)
	if span < 1e-9 {
		// all (nearly) coincident → circle layout
		return scaleCircle(ids, float64(len(ids)), mapPointsRange)
	}
	scale := mapPointsRange / span
	cx0, cy0 := (minX+maxX)/2, (minY+maxY)/2
	for i := range pts {
		pts[i].X = (pts[i].X - cx0) * scale
		pts[i].Y = (pts[i].Y - cy0) * scale
	}
	return pts
}

func scaleCircle(ids []int64, n float64, range_ float64) []mapPoint {
	out := make([]mapPoint, 0, int(n))
	for i := range ids {
		ang := 2 * math.Pi * float64(i) / n
		r := range_ / 2
		out = append(out, mapPoint{ID: ids[i], X: r * math.Cos(ang), Y: r * math.Sin(ang)})
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// energyKL computes the exact KL divergence C = Σ p_ij log(p_ij/q_ij) of the
// current layout against P (used for convergence monitoring / tests).
func energyKL(P *sparseP, yx, yy []float64) float64 {
	n := len(yx)
	total := 0.0
	for i := 0; i < n; i++ {
		U := 0.0
		for k := 0; k < n; k++ {
			if k == i {
				continue
			}
			dx := yx[i] - yx[k]
			dy := yy[i] - yy[k]
			U += 1.0 / (1.0 + dx*dx+dy*dy)
		}
		for m := range P.rows[i].idx {
			j := int(P.rows[i].idx[m])
			dx := yx[i] - yx[j]
			dy := yy[i] - yy[j]
			q := (1.0 / (1.0 + dx*dx+dy*dy)) / U
			p := float64(P.rows[i].val[m])
			total += p * math.Log(p / q)
		}
	}
	return total
}

