package keys

import (
	"sync"
	"testing"
)

func TestRegisterStore_GetUnsetReturnsEmpty(t *testing.T) {
	r := NewRegisterStore()
	if got := r.Get('a'); got != "" {
		t.Errorf("Get('a') on empty store = %q, want %q", got, "")
	}
}

func TestRegisterStore_SetGet(t *testing.T) {
	r := NewRegisterStore()
	r.Set('a', "hello")
	if got := r.Get('a'); got != "hello" {
		t.Errorf("Get('a') = %q, want %q", got, "hello")
	}
}

func TestRegisterStore_OverwriteSet(t *testing.T) {
	r := NewRegisterStore()
	r.Set('a', "v1")
	r.Set('a', "v2")
	if got := r.Get('a'); got != "v2" {
		t.Errorf("Get('a') after overwrite = %q, want %q", got, "v2")
	}
}

func TestRegisterStore_DistinctRegistersIndependent(t *testing.T) {
	r := NewRegisterStore()
	r.Set('a', "alpha")
	r.Set('b', "beta")
	if got := r.Get('a'); got != "alpha" {
		t.Errorf("Get('a') = %q, want %q", got, "alpha")
	}
	if got := r.Get('b'); got != "beta" {
		t.Errorf("Get('b') = %q, want %q", got, "beta")
	}
}

func TestRegisterStore_SetEmptyStringStored(t *testing.T) {
	r := NewRegisterStore()
	r.Set('a', "value")
	r.Set('a', "")
	if got := r.Get('a'); got != "" {
		t.Errorf("Get('a') after Set(a,\"\") = %q, want \"\"", got)
	}
}

func TestRegisterStore_ConcurrentSetGet(t *testing.T) {
	r := NewRegisterStore()
	var wg sync.WaitGroup
	const goroutines = 16
	const iterations = 500
	for i := 0; i < goroutines; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			reg := rune('a' + i)
			for n := 0; n < iterations; n++ {
				r.Set(reg, "v")
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			reg := rune('a' + i)
			for n := 0; n < iterations; n++ {
				_ = r.Get(reg)
			}
		}(i)
	}
	wg.Wait()
}
