package fixture

func Nested(value int) int {
	inner := func(candidate int) int {
		if candidate > 0 {
			return candidate
		}
		return 0
	}
	_ = inner
	return value
}
