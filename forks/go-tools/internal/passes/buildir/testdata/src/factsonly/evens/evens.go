package evens

type Pairs []string

func (p Pairs) Check() {
	if len(p)%2 != 0 {
		panic("odd")
	}
}

func Direct(kv []string) {
	if len(kv)%2 != 0 {
		panic("odd")
	}
}

func Variadic(kv ...string) {
	if len(kv)%2 == 1 {
		panic("odd")
	}
}

func Forward(kv []string) {
	Direct(kv)
}

func Generic[S ~[]E, E any](s S) {
	if len(s)%2 != 0 {
		panic("odd")
	}
}

func WithResult(kv []string) int {
	Direct(kv)
	return len(kv)
}

func Unrelated(n int) {
	_ = n * 2
}
