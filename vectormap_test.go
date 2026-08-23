package main

import (
	"math"
	"math/rand"
	"testing"
)

// fullKL computes the exact C = Σ_i Σ_j p_ij log(p_ij / q_ij) for a layout.
func fullKL(P *sparseP, yx, yy []float64) float64 {
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
			total += p * math.Log(p/q)
		}
	}
	return total
}

// randomX creates n deterministic d-dim points.
func randomX(n, d int, seed int64) [][]float32 {
	rng := rand.New(rand.NewSource(seed))
	X := make([][]float32, n)
	for i := range X {
		X[i] = make([]float32, d)
		for t := range X[i] {
			X[i][t] = float32(rng.NormFloat64())
		}
	}
	return X
}

func noopProgress(int) {}

// TestGradientFiniteDifference verifies the analytic gradient against
// central finite differences of the exact KL divergence.
func TestGradientFiniteDifference(t *testing.T) {
	n, d := 36, 10
	X := randomX(n, d, 7)
	perplexity := 8.0
	k := int(math.Round(math.Exp(perplexity)))
	if k > n-2 {
		k = n - 2
	}
	P, err := buildP(X, d, perplexity, k, noopProgress)
	if err != nil {
		t.Fatalf("buildP: %v", err)
	}

	// sanity: stored rowSum must match the actual CSR row sums
	for i := range P.rows {
		s := 0.0
		for m := range P.rows[i].val {
			s += float64(P.rows[i].val[m])
		}
		if math.Abs(s-P.rowSum[i]) > 1e-6*math.Max(1, s) {
			t.Fatalf("row %d sum mismatch: csr=%.8g rowSum=%.8g", i, s, P.rowSum[i])
		}
	}

	yx, yy := pcaProject2D(X, 42)
	i, comp := 5, 0
	U := make([]float64, n)
	S2xm := make([]float64, n)
	S2ym := make([]float64, n)
	momentsInto(yx, yy, U, S2xm, S2ym)
	gx, gy := gradientAt(i, yx[i], yy[i], P.rows[i], P.rowSum, U, S2xm, S2ym, yx, yy)

	eps := 1e-6
	coord := func() *float64 {
		if comp == 0 {
			return &yx[i]
		}
		return &yy[i]
	}()
	orig := *coord

	*coord = orig + eps
	cp := fullKL(P, yx, yy)
	*coord = orig - eps
	cm := fullKL(P, yx, yy)
	*coord = orig // restore

	fd := (cp - cm) / (2 * eps)
	got := gx
	if comp == 1 {
		got = gy
	}
	diff := math.Abs(fd-got)
	tol := 1e-4 * math.Max(1.0, math.Abs(fd))
	if diff > tol {
		t.Fatalf("finite difference mismatch: fd=%.8f analytic=%.8f (diff=%.2g)", fd, got, diff)
	}
}

// TestTSMNEEnergyDecreases runs gradient iterations and checks the KL
// divergence drops overall (early-exaggeration can briefly raise it).
func TestTSMNEEnergyDecreases(t *testing.T) {
	n, d := 64, 12
	X := randomX(n, d, 21)
	perplexity := 10.0
	k := int(math.Round(math.Exp(perplexity)))
	if k > n-2 {
		k = n - 2
	}
	P, err := buildP(X, d, perplexity, k, noopProgress)
	if err != nil {
		t.Fatalf("buildP: %v", err)
	}

	yx, yy := pcaProject2D(X, 42)
	e0 := fullKL(P, yx, yy)

	vx := make([]float64, n)
	vy := make([]float64, n)
	gx := make([]float64, n)
	gy := make([]float64, n)
	Um := make([]float64, n)
	S2xm := make([]float64, n)
	S2ym := make([]float64, n)
	cur := perplexityExaggerated(P, tsneEarlyExaggeration)
	momentum := 0.5
	for iter := 1; iter <= 250; iter++ {
		if iter > tsneExaggerateUntil {
			cur = P
		}
		if iter == tsneMomentumSwitch {
			momentum = 0.8
		}
		momentsInto(yx, yy, Um, S2xm, S2ym)
		for i := 0; i < n; i++ {
			gx[i], gy[i] = gradientAt(i, yx[i], yy[i], cur.rows[i], cur.rowSum, Um, S2xm, S2ym, yx, yy)
		}
		applyTSneUpdate(yx, yy, vx, vy, gx, gy, momentum)
	}
	e1 := fullKL(P, yx, yy)
	if !(e1 < e0) {
		t.Fatalf("energy did not decrease: e0=%.6f e1=%.6f", e0, e1)
	}
}

// TestBHTreeMatchesExact verifies the Barnes-Hut moments (U, S2) approximate
// the exact sums within a sane tolerance.
func TestBHTreeMatchesExact(t *testing.T) {
	n := 4000
	X := randomX(n, 8, 3)
	yx, yy := pcaProject2D(X, 11)

	tree := newTSneTree(n)
	tree.rebuild(yx, yy)

	i := 7
	// exact moments
	Ue, s2xe, s2ye := 0.0, 0.0, 0.0
	for k := 0; k < n; k++ {
		if k == i {
			continue
		}
		dx := yx[i] - yx[k]
		dy := yy[i] - yy[k]
		u := 1.0 / (1.0 + dx*dx+dy*dy)
		Ue += u
		s2xe += u * u * dx
		s2ye += u * u * dy
	}

	Ub, s2xb, s2yb := tree.query(yx[i], yy[i], i)
	if relErr := math.Abs(Ub-Ue) / Ue; relErr > 0.05 {
		t.Fatalf("U approximation too far off: exact=%.6f bh=%.6f (rel err %.3f)", Ue, Ub, relErr)
	}
	if relErr := math.Abs(s2xb-s2xe) / math.Max(1e-12, math.Abs(s2xe)); relErr > 0.15 {
		t.Fatalf("S2x approximation too far off: exact=%.6f bh=%.6f", s2xe, s2xb)
	}
	if relErr := math.Abs(s2yb-s2ye) / math.Max(1e-12, math.Abs(s2ye)); relErr > 0.15 {
		t.Fatalf("S2y approximation too far off: exact=%.6f bh=%.6f", s2ye, s2yb)
	}
}

// TestVectorCodecRoundTrip checks binary encode/decode symmetry.
func TestVectorCodecRoundTrip(t *testing.T) {
	in := []float32{0.1, -0.2, 3.14, math.SmallestNonzeroFloat32, -1e-30}
	out, err := decodeVector(encodeVector(in))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("len mismatch")
	}
	for i := range in {
		if out[i] != in[i] {
			t.Fatalf("roundtrip mismatch at %d: %v vs %v", i, out[i], in[i])
		}
	}
	if _, err := decodeVector(nil); err == nil {
		t.Fatal("expected error for empty blob")
	}
	if _, err := decodeVector([]byte{1, 2, 3}); err == nil {
		t.Fatal("expected error for odd-length blob")
	}
}

// TestHashEmbedDeterministic checks determinism, normalization and
// topical similarity (shared vocabulary → higher cosine).
func TestHashEmbedDeterministic(t *testing.T) {
	a1 := hashEmbed("Künstliche Intelligenz verändert die Softwareentwicklung")
	a2 := hashEmbed("Künstliche Intelligenz verändert die Softwareentwicklung")
	if a1 == nil || len(a1) != hashDim {
		t.Fatalf("bad dim")
	}
	for i := range a1 {
		if a1[i] != a2[i] {
			t.Fatal("hashEmbed is not deterministic")
		}
	}
	norm := 0.0
	for _, f := range a1 {
		norm += float64(f) * float64(f)
	}
	if math.Abs(norm-1) > 1e-5 {
		t.Fatalf("not L2-normalized: norm²=%.6f", norm)
	}

	b := hashEmbed("Künstliche Intelligenz und maschinelles Lernen in der Medizin")
	c := hashEmbed("Rezepte: Die besten Pfannkuchen mit Banane zum Frühstück")
	cosAB := dotF32(a1, b) / (math.Sqrt(norm) * 1.0)
	cosAC := dotF32(a1, c) / (math.Sqrt(norm) * 1.0)
	if cosAB <= cosAC {
		t.Fatalf("expected similar texts to score higher: AB=%.4f AC=%.4f", cosAB, cosAC)
	}
}

func dotF32(a, b []float32) float64 {
	s := 0.0
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// TestTSneProjectEndToEnd runs the full t-SNE core (BH path) and verifies the
// raw layout improves over the PCA initialization and stays finite.
func TestTSneProjectEndToEnd(t *testing.T) {
	n, d := 400, 16
	X := randomX(n, d, 99)

	yx0, yy0 := pcaProject2D(X, 42)

	yx, yy, err := tsneCore(X, d, noopProgress)
	if err != nil {
		t.Fatalf("tsneCore: %v", err)
	}
	if len(yx) != n || len(yy) != n {
		t.Fatalf("got %d/%d coords, want %d", len(yx), len(yy), n)
	}
	for i := 0; i < n; i++ {
		if math.IsNaN(yx[i]) || math.IsNaN(yy[i]) || math.IsInf(yx[i], 0) || math.IsInf(yy[i], 0) {
			t.Fatalf("non-finite output at %d: (%v,%v)", i, yx[i], yy[i])
		}
	}

	// Final layout must beat the PCA init on exact per-row KL.
	perplexity := math.Min(30, float64(n)/8)
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
	P, err := buildP(X, d, perplexity, k, noopProgress)
	if err != nil {
		t.Fatalf("buildP: %v", err)
	}
	eInit := fullKL(P, yx0, yy0)
	eFinal := fullKL(P, yx, yy)
	t.Logf("KL: pca-init=%.4f  t-sne=%.4f", eInit, eFinal)
	if eFinal >= eInit {
		t.Fatalf("t-SNE did not improve over PCA init: %.4f >= %.4f", eFinal, eInit)
	}
}

// TestUmapDeterministic verifies UMAP output is deterministic, finite and
// spread across the plane (not collapsed).
func TestUmapDeterministic(t *testing.T) {
	n, d := 250, 12
	X := randomX(n, d, 7)
	ids := make([]int64, n)
	for i := range ids {
		ids[i] = int64(i + 1)
	}

	p1, err := umapProject(ids, X, d, noopProgress)
	if err != nil {
		t.Fatalf("umapProject: %v", err)
	}
	p2, err := umapProject(ids, X, d, noopProgress)
	if err != nil {
		t.Fatalf("umapProject 2: %v", err)
	}
	for i := range p1 {
		if p1[i].ID != p2[i].ID || p1[i].X != p2[i].X || p1[i].Y != p2[i].Y {
			t.Fatalf("non-deterministic at %d: (%v,%v) vs (%v,%v)", i, p1[i].X, p1[i].Y, p2[i].X, p2[i].Y)
		}
		if math.IsNaN(p1[i].X) || math.IsNaN(p1[i].Y) || math.IsInf(p1[i].X, 0) || math.IsInf(p1[i].Y, 0) {
			t.Fatalf("non-finite at %d", i)
		}
	}
	var minX, maxX = math.Inf(1), math.Inf(-1)
	for _, p := range p1 {
		if p.X < minX {
			minX = p.X
		}
		if p.X > maxX {
			maxX = p.X
		}
	}
	t.Logf("x-span = %.1f (points=%d)", maxX-minX, len(p1))
	if maxX-minX < 10 {
		t.Fatalf("layout collapsed: x-span %.1f too small", maxX-minX)
	}
}
