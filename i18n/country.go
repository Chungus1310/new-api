package i18n

import "strings"

// countryNames covers the seat countries channels record; any other code is
// shown as is.
var countryNames = map[string]map[string]string{
	"en": {
		"AE": "United Arab Emirates", "AU": "Australia", "CA": "Canada", "CH": "Switzerland", "CN": "China",
		"DE": "Germany", "EE": "Estonia", "FI": "Finland", "FR": "France", "GB": "United Kingdom",
		"HK": "Hong Kong", "ID": "Indonesia", "IE": "Ireland", "IN": "India", "JP": "Japan",
		"KR": "South Korea", "LU": "Luxembourg", "MY": "Malaysia", "NL": "Netherlands", "PL": "Poland",
		"RU": "Russia", "SE": "Sweden", "SG": "Singapore", "TH": "Thailand", "TR": "Turkey",
		"TW": "Taiwan", "US": "United States", "VN": "Vietnam",
	},
	"zh-CN": {
		"AE": "阿联酋", "AU": "澳大利亚", "CA": "加拿大", "CH": "瑞士", "CN": "中国",
		"DE": "德国", "EE": "爱沙尼亚", "FI": "芬兰", "FR": "法国", "GB": "英国",
		"HK": "中国香港", "ID": "印度尼西亚", "IE": "爱尔兰", "IN": "印度", "JP": "日本",
		"KR": "韩国", "LU": "卢森堡", "MY": "马来西亚", "NL": "荷兰", "PL": "波兰",
		"RU": "俄罗斯", "SE": "瑞典", "SG": "新加坡", "TH": "泰国", "TR": "土耳其",
		"TW": "中国台湾", "US": "美国", "VN": "越南",
	},
	"zh-TW": {
		"AE": "阿拉伯聯合大公國", "AU": "澳洲", "CA": "加拿大", "CH": "瑞士", "CN": "中國",
		"DE": "德國", "EE": "愛沙尼亞", "FI": "芬蘭", "FR": "法國", "GB": "英國",
		"HK": "香港", "ID": "印尼", "IE": "愛爾蘭", "IN": "印度", "JP": "日本",
		"KR": "韓國", "LU": "盧森堡", "MY": "馬來西亞", "NL": "荷蘭", "PL": "波蘭",
		"RU": "俄羅斯", "SE": "瑞典", "SG": "新加坡", "TH": "泰國", "TR": "土耳其",
		"TW": "台灣", "US": "美國", "VN": "越南",
	},
}

// CountryName names an ISO 3166-1 alpha-2 code in the language, English when
// the language has no table.
func CountryName(lang, code string) string {
	code = strings.ToUpper(code)
	names, ok := countryNames[normalizeLang(lang)]
	if !ok {
		names = countryNames["en"]
	}
	if name, ok := names[code]; ok {
		return name
	}
	return code
}
