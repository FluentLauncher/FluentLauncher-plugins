package main

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxInput  = 8000
	maxOutput = 12000
)

type toolOutput struct {
	Title    string
	Subtitle string
	Text     string
}

func run(keyword, input string) []toolOutput {
	text := strings.TrimSpace(input)
	if len([]rune(text)) > maxInput {
		return []toolOutput{{Title: "输入过长", Subtitle: fmt.Sprintf("最多 %d 个字符，当前 %d 个", maxInput, len([]rune(text)))}}
	}
	switch strings.ToLower(keyword) {
	case "color":
		return colorTool(text)
	case "json":
		return jsonTool(text)
	case "base64":
		return base64Tool(text)
	case "urlencode":
		return urlTool(text)
	case "hash":
		return hashTool(text)
	case "regex", "regexp", "正则":
		return regexTool(text)
	}
	return []toolOutput{{Title: "未知工具", Subtitle: "可用触发词：json、base64、urlencode、hash、regex、color"}}
}

// regexTool matches "regex <pattern> <text>": it lists every match and the
// capture groups of the first one. Go's RE2 syntax applies, so lookaround and
// backreferences are out; the subtitle says what is copied.
func regexTool(text string) []toolOutput {
	pattern, target, found := strings.Cut(text, " ")
	if !found || strings.TrimSpace(target) == "" {
		return []toolOutput{{Title: "用法", Subtitle: "regex <正则> <文本>，例如 regex \\d+ abc123"}}
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return []toolOutput{{Title: "正则无效", Subtitle: err.Error()}}
	}
	matches := re.FindAllStringIndex(target, -1)
	if len(matches) == 0 {
		return []toolOutput{{Title: "无匹配", Subtitle: fmt.Sprintf("%d 个字符里没有匹配 %s", len([]rune(target)), pattern)}}
	}
	list := make([]string, 0, len(matches))
	for _, m := range matches {
		list = append(list, target[m[0]:m[1]])
	}
	joined := strings.Join(list, " | ")
	if len(joined) > maxOutput {
		joined = joined[:maxOutput] + "…"
	}
	outs := []toolOutput{output(fmt.Sprintf("匹配 %d 处", len(matches)), "Go RE2 语法 · 回车复制全部匹配（| 分隔）", joined)}
	first := re.FindStringSubmatchIndex(target)
	if len(first) > 2 {
		groups := make([]string, 0, len(first)/2-1)
		for i := 1; i*2+1 < len(first); i++ {
			if first[i*2] < 0 {
				groups = append(groups, fmt.Sprintf("%d: ∅", i))
				continue
			}
			groups = append(groups, fmt.Sprintf("%d: %s", i, target[first[i*2]:first[i*2+1]]))
		}
		outs = append(outs, output("首匹配捕获组", "输入 "+pattern, strings.Join(groups, "  ")))
	}
	return outs
}

func jsonTool(text string) []toolOutput {
	if text == "" {
		return []toolOutput{{Title: "等待输入", Subtitle: "输入 json 后跟要校验或格式化的内容"}}
	}
	cleaned := cleanJSON(text)
	decoder := json.NewDecoder(strings.NewReader(cleaned))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return []toolOutput{{Title: "JSON 无效", Subtitle: jsonErrorPosition(err), Text: err.Error()}}
	}
	formatted, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return []toolOutput{{Title: "JSON 无效", Subtitle: "无法格式化", Text: err.Error()}}
	}
	compact, err := json.Marshal(value)
	if err != nil {
		return []toolOutput{{Title: "JSON 无效", Subtitle: "无法压缩", Text: err.Error()}}
	}
	return []toolOutput{
		output("JSON 有效 · 已格式化", fmt.Sprintf("%s，%d 字符", jsonKind(value), len(compact)), string(formatted)),
		output("压缩为单行", "去掉空白", string(compact)),
	}
}

func jsonErrorPosition(err error) string {
	if syntax, ok := err.(*json.SyntaxError); ok && syntax.Offset > 0 {
		return fmt.Sprintf("第 %d 字节", syntax.Offset)
	}
	return "位置未知"
}

func jsonKind(value any) string {
	switch value.(type) {
	case map[string]any:
		return "Object"
	case []any:
		return "Array"
	case string:
		return "String"
	case json.Number:
		return "Number"
	case bool:
		return "Boolean"
	case nil:
		return "Null"
	}
	return "Value"
}

// cleanJSON removes // and /* */ comments and trailing commas outside
// strings, matching the C# parser's relaxed options.
func cleanJSON(text string) string {
	var out strings.Builder
	inString, escaped := false, false
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if inString {
			out.WriteByte(ch)
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inString = false
			}
			continue
		}
		if ch == '"' {
			inString = true
			out.WriteByte(ch)
			continue
		}
		if ch == '/' && i+1 < len(text) {
			if text[i+1] == '/' {
				for i < len(text) && text[i] != '\n' {
					i++
				}
				continue
			}
			if text[i+1] == '*' {
				i += 2
				for i+1 < len(text) && !(text[i] == '*' && text[i+1] == '/') {
					i++
				}
				i++
				continue
			}
		}
		out.WriteByte(ch)
	}
	cleaned := out.String()
	var result strings.Builder
	for i := 0; i < len(cleaned); i++ {
		if cleaned[i] == ',' {
			j := i + 1
			for j < len(cleaned) && (cleaned[j] == ' ' || cleaned[j] == '\t' || cleaned[j] == '\r' || cleaned[j] == '\n') {
				j++
			}
			if j < len(cleaned) && (cleaned[j] == '}' || cleaned[j] == ']') {
				continue
			}
		}
		result.WriteByte(cleaned[i])
	}
	return result.String()
}

func base64Tool(text string) []toolOutput {
	if text == "" {
		return []toolOutput{{Title: "等待输入", Subtitle: "输入 base64 后跟要编码或解码的内容"}}
	}
	results := make([]toolOutput, 0, 2)
	if decoded, ok := decodeBase64(text); ok {
		results = append(results, output("Base64 解码", "按 UTF-8 解释", decoded))
	}
	results = append(results, output("Base64 编码", "UTF-8 字节", base64.StdEncoding.EncodeToString([]byte(text))))
	return results
}

func decodeBase64(text string) (string, bool) {
	candidate := strings.NewReplacer("-", "+", "_", "/").Replace(text)
	if remainder := len(candidate) % 4; remainder != 0 {
		candidate += strings.Repeat("=", 4-remainder)
	}
	data, err := base64.StdEncoding.DecodeString(candidate)
	if err != nil || !utf8.Valid(data) || len(data) == 0 {
		return "", false
	}
	decoded := string(data)
	for _, r := range decoded {
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			return "", false
		}
	}
	return decoded, true
}

func urlTool(text string) []toolOutput {
	if text == "" {
		return []toolOutput{{Title: "等待输入", Subtitle: "输入 urlencode 后跟要编码或解码的内容"}}
	}
	results := []toolOutput{output("URL 编码", "保留字符按 RFC 3986 转义", escapeData(text))}
	if decoded, err := url.PathUnescape(text); err == nil && decoded != text {
		results = append([]toolOutput{output("URL 解码", "按 UTF-8 解释百分号转义", decoded)}, results...)
	}
	return results
}

func escapeData(text string) string {
	return strings.ReplaceAll(url.QueryEscape(text), "+", "%20")
}

func hashTool(text string) []toolOutput {
	if text == "" {
		return []toolOutput{{Title: "等待输入", Subtitle: "输入 hash 后要计算摘要的文本"}}
	}
	data := []byte(text)
	sum256 := sha256.Sum256(data)
	sum1 := sha1.Sum(data)
	sumMD5 := md5.Sum(data)
	return []toolOutput{
		output("SHA-256", "UTF-8 文本摘要", hex.EncodeToString(sum256[:])),
		output("SHA-1", "仅用于兼容旧系统，不要用于安全校验", hex.EncodeToString(sum1[:])),
		output("MD5", "仅用于兼容旧系统，不要用于安全校验", hex.EncodeToString(sumMD5[:])),
	}
}

var (
	hexColorRe = regexp.MustCompile(`^#?([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)
	rgbColorRe = regexp.MustCompile(`(?i)^(?:rgba?\s*\(?)?\s*(\d{1,3})\s*[,，\s]\s*(\d{1,3})\s*[,，\s]\s*(\d{1,3})\s*\)?$`)
	hslColorRe = regexp.MustCompile(`(?i)^hsla?\s*\(?\s*(\d{1,3}(?:\.\d+)?)\s*(?:deg)?\s*[,，\s]\s*(\d{1,3}(?:\.\d+)?)%?\s*[,，\s]\s*(\d{1,3}(?:\.\d+)?)%?\s*\)?$`)
)

// colorTool reads "#36a", "#3366AA", "rgb(51, 102, 170)", "51 102 170" or
// "hsl(214, 54%, 43%)" and lists the color in each notation.
func colorTool(text string) []toolOutput {
	if text == "" {
		return []toolOutput{{Title: "等待输入", Subtitle: "输入 color 后跟 #3366AA、rgb(51, 102, 170) 或 hsl(214, 54%, 43%)"}}
	}
	r, g, b, ok := parseColor(text)
	if !ok {
		return []toolOutput{{Title: "无法识别颜色", Subtitle: "支持 #RGB、#RRGGBB、rgb(r, g, b) 与 hsl(h, s%, l%)"}}
	}
	h, sat, l := rgbToHSL(r, g, b)
	hexValue := fmt.Sprintf("#%02X%02X%02X", r, g, b)
	return []toolOutput{
		output("HEX", "大写，CSS 与设计工具通用", hexValue),
		output("hex", "小写", strings.ToLower(hexValue)),
		output("RGB", "CSS rgb()", fmt.Sprintf("rgb(%d, %d, %d)", r, g, b)),
		output("HSL", "CSS hsl()", fmt.Sprintf("hsl(%d, %d%%, %d%%)", h, sat, l)),
		output("RGB 分量", "逗号分隔", fmt.Sprintf("%d, %d, %d", r, g, b)),
		output("0xRRGGBB", "代码中的整数常量", "0x"+hexValue[1:]),
	}
}

func parseColor(text string) (int, int, int, bool) {
	if m := hexColorRe.FindStringSubmatch(text); m != nil {
		digits := m[1]
		if len(digits) == 3 {
			digits = string([]byte{digits[0], digits[0], digits[1], digits[1], digits[2], digits[2]})
		}
		v, err := strconv.ParseUint(digits, 16, 32)
		if err != nil {
			return 0, 0, 0, false
		}
		return int(v >> 16 & 0xff), int(v >> 8 & 0xff), int(v & 0xff), true
	}
	if m := hslColorRe.FindStringSubmatch(text); m != nil {
		h, _ := strconv.ParseFloat(m[1], 64)
		sat, _ := strconv.ParseFloat(m[2], 64)
		l, _ := strconv.ParseFloat(m[3], 64)
		if h > 360 || sat > 100 || l > 100 {
			return 0, 0, 0, false
		}
		r, g, b := hslToRGB(h, sat/100, l/100)
		return r, g, b, true
	}
	if m := rgbColorRe.FindStringSubmatch(text); m != nil {
		r, _ := strconv.Atoi(m[1])
		g, _ := strconv.Atoi(m[2])
		b, _ := strconv.Atoi(m[3])
		if r > 255 || g > 255 || b > 255 {
			return 0, 0, 0, false
		}
		return r, g, b, true
	}
	return 0, 0, 0, false
}

func rgbToHSL(r, g, b int) (int, int, int) {
	rf, gf, bf := float64(r)/255, float64(g)/255, float64(b)/255
	mx := math.Max(rf, math.Max(gf, bf))
	mn := math.Min(rf, math.Min(gf, bf))
	l := (mx + mn) / 2
	if mx == mn {
		return 0, 0, int(math.Round(l * 100))
	}
	d := mx - mn
	s := d / (1 - math.Abs(2*l-1))
	var h float64
	switch mx {
	case rf:
		h = math.Mod((gf-bf)/d, 6)
	case gf:
		h = (bf-rf)/d + 2
	default:
		h = (rf-gf)/d + 4
	}
	h *= 60
	if h < 0 {
		h += 360
	}
	return int(math.Round(h)), int(math.Round(s * 100)), int(math.Round(l * 100))
}

func hslToRGB(h, s, l float64) (int, int, int) {
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2
	var r, g, b float64
	switch {
	case h < 60:
		r, g = c, x
	case h < 120:
		r, g = x, c
	case h < 180:
		g, b = c, x
	case h < 240:
		g, b = x, c
	case h < 300:
		r, b = x, c
	default:
		r, b = c, x
	}
	round := func(v float64) int { return int(math.Round((v + m) * 255)) }
	return round(r), round(g), round(b)
}

func output(title, subtitle, text string) toolOutput {
	runes := []rune(text)
	if len(runes) > maxOutput {
		text = string(runes[:maxOutput]) + fmt.Sprintf("…（已截断，共 %d 字符）", len(runes))
	}
	return toolOutput{Title: title, Subtitle: subtitle, Text: text}
}
