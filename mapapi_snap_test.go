package main

import (
	"database/sql"
	"math"
	"testing"

	_ "modernc.org/sqlite"
)

func testPositionsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := ensureMapPositionsTable(db); err != nil {
		t.Fatalf("ensure table: %v", err)
	}
	return db
}

func TestAnchorPointCentroid(t *testing.T) {
	// Vier Anker in den Ecken eines Quadrats; neuer Punkt exakt in der Mitte.
	old := [][]float32{{1, 1}, {1, -1}, {-1, 1}, {-1, -1}}
	pos := []mapPoint{
		{ID: 1, X: 1, Y: 1}, {ID: 2, X: 1, Y: -1},
		{ID: 3, X: -1, Y: 1}, {ID: 4, X: -1, Y: -1},
	}
	p := anchorPoint(99, []float32{0, 0}, old, pos)
	if math.Abs(p.X) > 0.6 || math.Abs(p.Y) > 0.6 {
		t.Fatalf("anchored point (%v, %v) should stay near the center", p.X, p.Y)
	}
	if p.ID != 99 {
		t.Fatalf("expected ID 99, got %d", p.ID)
	}
	// Jitter ist deterministisch: zweiter Lauf liefert dieselbe Position.
	p2 := anchorPoint(99, []float32{0, 0}, old, pos)
	if p.X != p2.X || p.Y != p2.Y {
		t.Fatalf("anchor jitter not deterministic: (%v,%v) vs (%v,%v)", p.X, p.Y, p2.X, p2.Y)
	}
}

func TestAnchorPointDominantNeighbor(t *testing.T) {
	// Ein sehr naher Nachbar muss die Position dominieren.
	old := [][]float32{{0.01, 0}, {10, 10}, {-10, -10}}
	pos := []mapPoint{
		{ID: 1, X: 5, Y: 5}, {ID: 2, X: 50, Y: 50}, {ID: 3, X: -50, Y: -50},
	}
	p := anchorPoint(7, []float32{0, 0}, old, pos)
	if math.Hypot(p.X-5, p.Y-5) > 1.5 {
		t.Fatalf("expected position near dominant neighbor (5,5), got (%v,%v)", p.X, p.Y)
	}
}

func TestSnapNewPointsIncremental(t *testing.T) {
	const oldN = 20
	db := testPositionsDB(t)

	// Bestehendes Layout: 20 Anker mit deterministischen Vektoren/Positionen.
	oldX := randomX(oldN, 8, 42)
	oldP := make([]mapPoint, oldN)
	for i := range oldP {
		oldP[i] = mapPoint{ID: int64(i + 1), X: float64(i%7) - 3, Y: float64(i%5) * 0.3}
	}
	fpOld := vectorFingerprint(idsOf(oldP), "test-model")
	if err := storePositions(db, oldP, "test-model", fpOld); err != nil {
		t.Fatalf("store old positions: %v", err)
	}

	app := &App{db: db, cfg: Config{}} // leeres Config → LocalHashModel

	// 3 neue Punkte (IDs 101–103), Vektoren deutlich anders als die Anker.
	newX := randomX(3, 8, 7)
	ids := make([]int64, 0, oldN+3)
	X := make([][]float32, 0, oldN+3)
	for i := 0; i < oldN; i++ {
		ids = append(ids, int64(i+1))
		X = append(X, oldX[i])
	}
	for i := 0; i < 3; i++ {
		ids = append(ids, int64(101+i))
		X = append(X, newX[i])
	}
	fpNew := vectorFingerprint(append([]int64(nil), ids...), "test-model")

	snapped, handled, err := app.snapNewPoints(ids, X, "test-model", fpNew)
	if err != nil {
		t.Fatalf("snap: %v", err)
	}
	if !handled || snapped != 3 {
		t.Fatalf("expected handled=true snapped=3, got handled=%v snapped=%d", handled, snapped)
	}

	all, err := loadAllStoredPositions(db)
	if err != nil {
		t.Fatalf("load stored: %v", err)
	}
	if len(all) != oldN+3 {
		t.Fatalf("expected %d stored positions, got %d", oldN+3, len(all))
	}
	for _, id := range []int64{101, 102, 103} {
		p, ok := all[id]
		if !ok {
			t.Fatalf("new point %d missing from stored layout", id)
		}
		if math.IsNaN(p.X) || math.IsNaN(p.Y) || math.IsInf(p.X, 0) || math.IsInf(p.Y, 0) {
			t.Fatalf("new point %d has non-finite position", id)
		}
	}
	// Alte Punkte behalten exakt ihre Positionen.
	if all[1] != oldP[0] || all[oldN] != oldP[oldN-1] {
		t.Fatalf("existing positions were modified")
	}
	// Unter dem neuen Fingerprint ist die Karte "ready".
	if _, ok, _ := loadCachedPositions(db, fpNew); !ok {
		t.Fatalf("stored layout not found under new fingerprint")
	}
}

func TestSnapNewPointsBatchTooLarge(t *testing.T) {
	db := testPositionsDB(t)
	const oldN = 40
	oldX := randomX(oldN, 8, 1)
	oldP := make([]mapPoint, oldN)
	for i := range oldP {
		oldP[i] = mapPoint{ID: int64(i + 1), X: float64(i) * 0.1, Y: float64(i) * -0.1}
	}
	if err := storePositions(db, oldP, "test-model", "fp-old"); err != nil {
		t.Fatalf("store: %v", err)
	}
	app := &App{db: db, cfg: Config{}}

	// snapBatchLimit(40) = 25 → 30 neue Punkte müssen die volle Pipeline erzwingen.
	bigX := randomX(30, 8, 2)
	ids := make([]int64, 0, oldN+30)
	X := make([][]float32, 0, oldN+30)
	for i := 0; i < oldN; i++ {
		ids = append(ids, int64(i+1))
		X = append(X, oldX[i])
	}
	for i := 0; i < 30; i++ {
		ids = append(ids, int64(500+i))
		X = append(X, bigX[i])
	}
	if _, handled, err := app.snapNewPoints(ids, X, "test-model", "fp-new"); err != nil || handled {
		t.Fatalf("expected handled=false for oversized batch, got handled=%v err=%v", handled, err)
	}
}

func TestSnapNewPointsNoLayoutYet(t *testing.T) {
	db := testPositionsDB(t) // leere map_positions
	app := &App{db: db, cfg: Config{}}
	X := randomX(30, 8, 3)
	ids := make([]int64, 30)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	if _, handled, err := app.snapNewPoints(ids, X, "test-model", "fp"); err != nil || handled {
		t.Fatalf("expected handled=false without existing layout, got handled=%v err=%v", handled, err)
	}
}

func idsOf(ps []mapPoint) []int64 {
	ids := make([]int64, len(ps))
	for i, p := range ps {
		ids[i] = p.ID
	}
	return ids
}

