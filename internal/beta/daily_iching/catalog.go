package dailyiching

type hexagramMeta struct {
	Number int    `json:"number"`
	Name   string `json:"name"`
	Title  string `json:"title"`
}

var kingWenSequence = []hexagramMeta{
	{Number: 1, Name: "Càn", Title: "Thuần Càn"},
	{Number: 2, Name: "Khôn", Title: "Thuần Khôn"},
	{Number: 3, Name: "Truân", Title: "Thủy Lôi Truân"},
	{Number: 4, Name: "Mông", Title: "Sơn Thủy Mông"},
	{Number: 5, Name: "Nhu", Title: "Thủy Thiên Nhu"},
	{Number: 6, Name: "Tụng", Title: "Thiên Thủy Tụng"},
	{Number: 7, Name: "Sư", Title: "Địa Thủy Sư"},
	{Number: 8, Name: "Tỷ", Title: "Thủy Địa Tỷ"},
	{Number: 9, Name: "Tiểu Súc", Title: "Phong Thiên Tiểu Súc"},
	{Number: 10, Name: "Lý", Title: "Thiên Trạch Lý"},
	{Number: 11, Name: "Thái", Title: "Địa Thiên Thái"},
	{Number: 12, Name: "Bĩ", Title: "Thiên Địa Bĩ"},
	{Number: 13, Name: "Đồng Nhân", Title: "Thiên Hỏa Đồng Nhân"},
	{Number: 14, Name: "Đại Hữu", Title: "Hỏa Thiên Đại Hữu"},
	{Number: 15, Name: "Khiêm", Title: "Địa Sơn Khiêm"},
	{Number: 16, Name: "Dự", Title: "Lôi Địa Dự"},
	{Number: 17, Name: "Tùy", Title: "Trạch Lôi Tùy"},
	{Number: 18, Name: "Cổ", Title: "Sơn Phong Cổ"},
	{Number: 19, Name: "Lâm", Title: "Địa Trạch Lâm"},
	{Number: 20, Name: "Quán", Title: "Phong Địa Quán"},
	{Number: 21, Name: "Phệ Hạp", Title: "Hỏa Lôi Phệ Hạp"},
	{Number: 22, Name: "Bí", Title: "Sơn Hỏa Bí"},
	{Number: 23, Name: "Bác", Title: "Sơn Địa Bác"},
	{Number: 24, Name: "Phục", Title: "Địa Lôi Phục"},
	{Number: 25, Name: "Vô Vọng", Title: "Thiên Lôi Vô Vọng"},
	{Number: 26, Name: "Đại Súc", Title: "Sơn Thiên Đại Súc"},
	{Number: 27, Name: "Di", Title: "Sơn Lôi Di"},
	{Number: 28, Name: "Đại Quá", Title: "Trạch Phong Đại Quá"},
	{Number: 29, Name: "Khảm", Title: "Thuần Khảm"},
	{Number: 30, Name: "Ly", Title: "Thuần Ly"},
	{Number: 31, Name: "Hàm", Title: "Trạch Sơn Hàm"},
	{Number: 32, Name: "Hằng", Title: "Lôi Phong Hằng"},
	{Number: 33, Name: "Độn", Title: "Thiên Sơn Độn"},
	{Number: 34, Name: "Đại Tráng", Title: "Lôi Thiên Đại Tráng"},
	{Number: 35, Name: "Tấn", Title: "Hỏa Địa Tấn"},
	{Number: 36, Name: "Minh Di", Title: "Địa Hỏa Minh Di"},
	{Number: 37, Name: "Gia Nhân", Title: "Phong Hỏa Gia Nhân"},
	{Number: 38, Name: "Khuê", Title: "Hỏa Trạch Khuê"},
	{Number: 39, Name: "Kiển", Title: "Thủy Sơn Kiển"},
	{Number: 40, Name: "Giải", Title: "Lôi Thủy Giải"},
	{Number: 41, Name: "Tổn", Title: "Sơn Trạch Tổn"},
	{Number: 42, Name: "Ích", Title: "Phong Lôi Ích"},
	{Number: 43, Name: "Quải", Title: "Trạch Thiên Quải"},
	{Number: 44, Name: "Cấu", Title: "Thiên Phong Cấu"},
	{Number: 45, Name: "Tụy", Title: "Trạch Địa Tụy"},
	{Number: 46, Name: "Thăng", Title: "Địa Phong Thăng"},
	{Number: 47, Name: "Khốn", Title: "Trạch Thủy Khốn"},
	{Number: 48, Name: "Tỉnh", Title: "Thủy Phong Tỉnh"},
	{Number: 49, Name: "Cách", Title: "Trạch Hỏa Cách"},
	{Number: 50, Name: "Đỉnh", Title: "Hỏa Phong Đỉnh"},
	{Number: 51, Name: "Chấn", Title: "Thuần Chấn"},
	{Number: 52, Name: "Cấn", Title: "Thuần Cấn"},
	{Number: 53, Name: "Tiệm", Title: "Phong Sơn Tiệm"},
	{Number: 54, Name: "Quy Muội", Title: "Lôi Trạch Quy Muội"},
	{Number: 55, Name: "Phong", Title: "Lôi Hỏa Phong"},
	{Number: 56, Name: "Lữ", Title: "Hỏa Sơn Lữ"},
	{Number: 57, Name: "Tốn", Title: "Thuần Tốn"},
	{Number: 58, Name: "Đoài", Title: "Thuần Đoài"},
	{Number: 59, Name: "Hoán", Title: "Phong Thủy Hoán"},
	{Number: 60, Name: "Tiết", Title: "Thủy Trạch Tiết"},
	{Number: 61, Name: "Trung Phu", Title: "Phong Trạch Trung Phu"},
	{Number: 62, Name: "Tiểu Quá", Title: "Lôi Sơn Tiểu Quá"},
	{Number: 63, Name: "Ký Tế", Title: "Thủy Hỏa Ký Tế"},
	{Number: 64, Name: "Vị Tế", Title: "Hỏa Thủy Vị Tế"},
}

func hexagramByNumber(number int) (hexagramMeta, bool) {
	if number < 1 || number > len(kingWenSequence) {
		return hexagramMeta{}, false
	}
	return kingWenSequence[number-1], true
}

func hexagramAtSequenceIndex(index int) (hexagramMeta, bool) {
	return hexagramByNumber(index)
}

func nextSequenceIndex(current int) int {
	switch {
	case current <= 0:
		return 1
	case current >= len(kingWenSequence):
		return 1
	default:
		return current + 1
	}
}

func previousSequenceIndex(current int) int {
	switch {
	case current <= 1:
		return len(kingWenSequence)
	case current > len(kingWenSequence):
		return len(kingWenSequence)
	default:
		return current - 1
	}
}
