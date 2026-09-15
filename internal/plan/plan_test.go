package plan

import "testing"

func TestBuildPlan(t *testing.T) {
	cases := []struct {
		name  string
		total int64
		n     int
	}{
		{"even", 1000, 10},
		{"odd", 1003, 10},
		{"single", 1000, 1},
		{"more segments than bytes", 5, 10},
		{"prime", 7919, 64},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := BuildPlan(tc.total, tc.n)
			var sum int64
			for i, c := range p {
				if c.Start != sum {
					t.Fatalf("segment %d start=%d want %d (contiguity)", i, c.Start, sum)
				}
				if c.End < c.Start {
					t.Fatalf("segment %d end<start", i)
				}
				if c.Position != i {
					t.Fatalf("segment %d position=%d", i, c.Position)
				}
				sum = c.End
			}
			if sum != tc.total {
				t.Fatalf("plan covers %d bytes, want %d", sum, tc.total)
			}
		})
	}
}

func TestReinterleavePreservesProgress(t *testing.T) {
	total := int64(1000)
	old := BuildPlan(total, 4)
	// 假设前两段已完成、第三段完成一半。
	old[0].Downloaded = old[0].Size()
	old[1].Downloaded = old[1].Size()
	old[2].Downloaded = old[2].Size() / 2

	want := SumDownloaded(old)
	got := SumDownloaded(Reinterleave(old, total, 8))
	if got != want {
		t.Fatalf("reinterleave changed downloaded total: got %d want %d", got, want)
	}
	// 切成更少分片也必须保留总量。
	got = SumDownloaded(Reinterleave(old, total, 2))
	if got != want {
		t.Fatalf("reinterleave(2) changed downloaded total: got %d want %d", got, want)
	}
}

func TestAutoSegmentCount(t *testing.T) {
	// 极小文件：不该切成 8 片。
	if n := AutoSegmentCount(100, 8, 1024); n != 1 {
		t.Fatalf("tiny file got %d segments, want 1", n)
	}
	// 大文件：每个连接一片。
	if n := AutoSegmentCount(1<<30, 8, 1<<20); n != 8 {
		t.Fatalf("large file got %d segments, want 8", n)
	}
}
