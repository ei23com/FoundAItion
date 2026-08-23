package main

// ─── UMAP projection ─────────────────────────────────────────────────────────
//
// Uniform Manifold Approximation and Projection (McInnes et al., 2018),
// reduced to the deterministic core used by FoundAItion's topic map:
//
//  1. k-nearest-neighbour graph on the embedding vectors (euclidean).
//  2. Fuzzy-simplicial-set weights: per point a local connectivity scale σ_i
//     is found by binary search so that Σ exp(-(d_ij − ρ_i)/σ_i) = log2(k),
//     where ρ_i is the distance to the nearest neighbour. This keeps dense
//     regions tight and sparse regions loose — the property that gives UMAP
//     its "landscape" look instead of t-SNE's round cloud.
//  3. Fuzzy union of the directed graph (weight = max of both directions).
//  4. SGD layout optimization: attractive forces along graph edges,
//     repulsion via negative sampling, with the standard
//     curve (a, b) fitted for min_dist=0.1 / spread=1.
//
// Everything runs from a fixed seed: identical input → identical map.

import (
	"fmt"
	"log"
	"math"
	"sort"
)

const (
	umapNeighbors       = 15
	umapEpochs          = 500
	umapNegativeSamples = 5
	umapGamma           = 1.0
	umapInitialAlpha    = 1.0
	umapA               = 1.5776361408408847 // fit(min_dist=0.1, spread=1)
	umapB               = 0.8951505208980053
	umapGradClip        = 4.0
	mapLayoutAlgoID     = "umap-v1"
)

type umapEdge struct {
	i, j int32
	w    float32
}

// lcg is a tiny deterministic PRNG (linear congruential generator).
type lcg struct{ s uint64 }

func newLCG(seed uint64) *lcg { return &lcg{s: seed} }

func (r *lcg) next() uint64 {
	r.s = r.s*6364136223846793005 + 1442695040888963407
	return r.s >> 17
}

// nextN returns a value in [0, n).
func (r *lcg) nextN(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next() % uint64(n))
}

// knnGraph computes the k nearest neighbours (euclidean) of every row in X.
func knnGraph(X [][]float32, d, k int, onProgress func(pct int)) (idx [][]int32, dist [][]float32) {
	n := len(X)
	if k > n-1 {
		k = n - 1
	}
	idx = make([][]int32, n)
	dist = make([][]float32, n)
	row := make([]float64, n)
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			if i == j {
				row[j] = math.Inf(1)
				continue
			}
			s := float64(0)
			xi, xj := X[i], X[j]
			for t := 0; t < d; t++ {
				df := float64(xi[t]) - float64(xj[t])
				s += df * df
			}
			row[j] = s
		}
		ord := make([]int, n)
		for j := range ord {
			ord[j] = j
		}
		sort.Slice(ord, func(a, b int) bool { return row[ord[a]] < row[ord[b]] })
		idx[i] = make([]int32, k)
		dist[i] = make([]float32, k)
		for t := 0; t < k; t++ {
			idx[i][t] = int32(ord[t])
			dist[i][t] = float32(math.Sqrt(row[ord[t]]))
		}
		if i%50 == 0 {
			onProgress(10 + 40*(i+1)/n)
		}
	}
	return idx, dist
}

// buildFuzzyGraph converts the kNN graph into the undirected fuzzy simplicial
// set edge list using the smooth-knn normalisation.
func buildFuzzyGraph(idx [][]int32, dist [][]float32, onProgress func(pct int)) []umapEdge {
	n := len(idx)
	k := len(idx[0])
	target := math.Log2(float64(k))
	type dirEdge struct{ i, j int32; w float32 }
	directed := make([]dirEdge, 0, n*k)

	for i := 0; i < n; i++ {
		rho := float64(dist[i][0])
		// Binary search σ so that Σ_t exp(-(d_t − ρ)/σ) ≈ log2(k).
		lo, hi := 1e-8, 1e3
		for iter := 0; iter < 64; iter++ {
			mid := (lo + hi) / 2
			psum := 0.0
			for t := 0; t < k; t++ {
				dd := float64(dist[i][t]) - rho
				if dd <= 0 {
					psum++
				} else {
					psum += math.Exp(-dd / mid)
				}
			}
			if psum > target {
				hi = mid
			} else {
				lo = mid
			}
		}
		sigma := (lo + hi) / 2

		for t := 0; t < k; t++ {
			j := idx[i][t]
			if int(j) == i {
				continue
			}
			dd := float64(dist[i][t]) - rho
			w := math.Exp(-dd / sigma)
			if w < 1e-6 {
				continue
			}
			directed = append(directed, dirEdge{i: int32(i), j: j, w: float32(w)})
		}
		if i%50 == 0 {
			onProgress(52 + 18*(i+1)/n)
		}
	}

	// Fuzzy union: weight(i,j) = max(w_ij, w_ji). Deduplicate via key map.
	type key struct{ a, b int32 }
	union := make(map[key]float32, len(directed)*2)
	for _, e := range directed {
		var kk key
		if e.i < e.j {
			kk = key{e.i, e.j}
		} else {
			kk = key{e.j, e.i}
		}
		if union[kk] < e.w {
			union[kk] = e.w
		}
	}

	edges := make([]umapEdge, 0, len(union))
	for kk, w := range union {
		edges = append(edges, umapEdge{i: kk.a, j: kk.b, w: w})
	}
	sort.Slice(edges, func(a, b int) bool {
		if edges[a].i != edges[b].i {
			return edges[a].i < edges[b].i
		}
		return edges[a].j < edges[b].j
	})
	onProgress(72)
	return edges
}

// clipUmap clamps v componentwise to ±umapGradClip.
func clipUmap(v float64) float64 {
	if v > umapGradClip {
		return umapGradClip
	}
	if v < -umapGradClip {
		return -umapGradClip
	}
	return v
}

// optimizeLayout runs UMAP's SGD loop on yx/yy in place.
func optimizeLayout(yx, yy []float64, edges []umapEdge, onProgress func(pct int)) {
	n := len(yx)
	rng := newLCG(42)
	alpha := umapInitialAlpha

	for ep := 0; ep < umapEpochs; ep++ {
		alpha = umapInitialAlpha * (1.0 - float64(ep)/float64(umapEpochs))
		if alpha < 0.01 {
			alpha = 0.01
		}

		// Deterministic shuffle for better mixing without biasing direction.
		for i := len(edges) - 1; i > 0; i-- {
			j := rng.nextN(i + 1)
			edges[i], edges[j] = edges[j], edges[i]
		}

		for _, e := range edges {
			i, j := int(e.i), int(e.j)
			dx := yx[i] - yx[j]
			dy := yy[i] - yy[j]
			d2 := dx*dx + dy*dy
			if d2 > 0 {
				coeff := -2.0 * umapA * umapB * math.Pow(d2, umapB-1) /
					(umapA*math.Pow(d2, umapB) + 1.0)
				gx := alpha * clipUmap(coeff * dx)
				gy := alpha * clipUmap(coeff * dy)
				yx[i] += gx
				yy[i] += gy
				yx[j] -= gx
				yy[j] -= gy
			}
			for s := 0; s < umapNegativeSamples; s++ {
				nj := rng.nextN(n)
				if nj == i || nj == j {
					continue
				}
				ndx := yx[i] - yx[nj]
				ndy := yy[i] - yy[nj]
				nd2 := ndx*ndx + ndy*ndy
				if nd2 == 0 {
					continue
				}
				coeff := 2.0 * umapGamma * umapB /
					((0.001 + nd2) * (umapA*math.Pow(nd2, umapB) + 1.0))
				yx[i] += alpha * clipUmap(coeff*ndx)
				yy[i] += alpha * clipUmap(coeff*ndy)
			}
		}

		if ep%25 == 0 || ep == umapEpochs-1 {
			p := 75 + 24*((ep+1)/umapEpochs)
			onProgress(p)
		}
	}
}

// umapProject computes the full UMAP layout and returns display-ready points.
func umapProject(ids []int64, X [][]float32, d int, onProgress func(pct int)) ([]mapPoint, error) {
	n := len(X)
	k := umapNeighbors
	if k > n-1 {
		k = n - 1
	}
	if k < 2 {
		k = 2
	}

	idx, dist := knnGraph(X, d, k, onProgress)
	edges := buildFuzzyGraph(idx, dist, onProgress)
	if len(edges) == 0 {
		return nil, fmt.Errorf("leerer Graph – keine Nachbarn gefunden")
	}

	// PCA init scaled to a moderate box (span ≈ 20).
	pcx, pcy := pcaProject2D(X, 42)
	yx := make([]float64, n)
	yy := make([]float64, n)
	minX, maxX, minY, maxY := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	for i := 0; i < n; i++ {
		yx[i] = pcx[i]
		yy[i] = pcy[i]
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
	sx := 20.0 / math.Max(maxX-minX, 1e-9)
	sy := 20.0 / math.Max(maxY-minY, 1e-9)
	for i := 0; i < n; i++ {
		yx[i] = (yx[i] - (minX+maxX)/2) * sx
		yy[i] = (yy[i] - (minY+maxY)/2) * sy
	}

	optimizeLayout(yx, yy, edges, onProgress)
	log.Printf("[map] UMAP done: %d points, %d edges, %d epochs", n, len(edges), umapEpochs)

	out := make([]mapPoint, n)
	for i := range ids {
		out[i] = mapPoint{ID: ids[i], X: yx[i], Y: yy[i]}
	}
	return normalizePoints(ids, out), nil
}
