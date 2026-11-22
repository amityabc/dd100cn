package protocol

// RegisteredRequest matches the configured C# route registry before any session
// wrappers run. The upstream dispatcher observes unknown frames and keeps reading.
func RegisteredRequest(f Frame, delegated bool) bool {
	if f.Direction != ClientToServer {
		return false
	}
	if f.Transport == Channel {
		return f.Class == 0 && (f.Opcode == 9 || f.Opcode == 11 || f.Opcode == 17)
	}
	if f.Transport != Game || f.Class != 1 {
		return false
	}
	switch f.Opcode {
	case 72, 450, 1645, 1701, 1702, 1703, 1719, 1875:
		return delegated
	case 3, 4, 5, 7, 8, 12, 13, 15, 16, 18, 19, 20, 21, 22, 23, 26, 27, 28, 29, 30,
		31, 32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 50, 52,
		63, 64, 69, 70, 71, 80, 81, 94, 95, 96, 99, 100, 102, 117, 120, 132, 134, 135, 143,
		160, 176, 191, 194, 195, 197, 198, 201, 202, 205, 206, 211, 212, 217, 237, 256, 260, 272,
		305, 306, 307, 308, 315, 324, 331, 393, 400, 410, 422, 430, 439, 449, 451, 467, 483,
		490, 494, 501, 507, 510, 527, 623, 679, 681, 692, 707, 777, 857, 897, 907, 908, 972,
		1113, 1122, 1261, 1279, 1418, 1421, 1422, 1449, 1715, 1733, 1734, 1735, 1736, 1767,
		1768, 1772, 1786, 1787, 1826, 1829, 1830, 1880, 1894, 1895, 1896, 1918, 1919, 1929, 1930:
		return true
	}
	return false
}
