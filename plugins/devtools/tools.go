package main

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
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
	case "time":
		return timeTool(text)
	case "uuid":
		return uuidTool(text)
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
	return []toolOutput{{Title: "未知工具", Subtitle: "可用触发词：time、uuid、json、base64、urlencode、hash、regex"}}
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

func timeTool(text string) []toolOutput {
	now := time.Now()
	if text == "" {
		return []toolOutput{
			output("当前时间", "本机时区 "+offset(now), now.Format("2006-01-02 15:04:05.000 Z07:00")),
			output("当前时间（UTC）", "UTC+00:00", now.UTC().Format("2006-01-02 15:04:05.000Z")),
			output("Unix 秒", "自 1970-01-01 UTC", strconv.FormatInt(now.Unix(), 10)),
			output("Unix 毫秒", "自 1970-01-01 UTC", strconv.FormatInt(now.UnixMilli(), 10)),
		}
	}
	if number, err := strconv.ParseInt(text, 10, 64); err == nil {
		milliseconds := number >= 100_000_000_000 || number <= -100_000_000_000
		var moment time.Time
		if milliseconds {
			moment = time.UnixMilli(number)
		} else {
			moment = time.Unix(number, 0)
		}
		local := moment.Local()
		label := "秒"
		if milliseconds {
			label = "毫秒"
		}
		return []toolOutput{
			output(fmt.Sprintf("%s时间戳 → 本机时区 %s", label, offset(local)), "输入 "+text, local.Format("2006-01-02 15:04:05.000 Z07:00")),
			output("→ UTC", "UTC+00:00", moment.UTC().Format("2006-01-02 15:04:05.000Z")),
			output("→ ISO 8601", "带时区偏移", local.Format(time.RFC3339Nano)),
		}
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02", time.RFC3339Nano} {
		parsed, err := time.ParseInLocation(layout, text, time.Local)
		if err != nil {
			continue
		}
		assumed := !strings.ContainsAny(text, "Zz+") && strings.LastIndex(text, "-") <= 7
		subtitle := "按输入中的时区偏移"
		if assumed {
			subtitle = "输入未带时区，按本机时区 " + offset(parsed) + " 解释"
		}
		return []toolOutput{
			output("Unix 秒", subtitle, strconv.FormatInt(parsed.Unix(), 10)),
			output("Unix 毫秒", subtitle, strconv.FormatInt(parsed.UnixMilli(), 10)),
			output("→ UTC", "UTC+00:00", parsed.UTC().Format("2006-01-02 15:04:05.000Z")),
		}
	}
	return []toolOutput{{Title: "无法识别时间", Subtitle: "支持 Unix 秒/毫秒，或 2026-11-06 18:30 这类日期时间"}}
}

func offset(t time.Time) string {
	_, seconds := t.Zone()
	sign := "+"
	if seconds < 0 {
		sign = "-"
		seconds = -seconds
	}
	return fmt.Sprintf("%s%02d:%02d", sign, seconds/3600, seconds%3600/60)
}

func uuidTool(text string) []toolOutput {
	count := 1
	if value, err := strconv.Atoi(text); err == nil {
		count = min(max(value, 1), 20)
	}
	values := make([]string, count)
	for i := range values {
		values[i] = newUUID()
	}
	subtitle := "随机生成"
	if count > 1 {
		subtitle = fmt.Sprintf("%d 个，随机生成", count)
	}
	results := []toolOutput{output("UUID v4", subtitle, strings.Join(values, "\r\n"))}
	if count == 1 {
		results = append(results,
			output("无连字符", "32 位十六进制", strings.ReplaceAll(values[0], "-", "")),
			output("大写", "常见于注册表与配置", strings.ToUpper(values[0])))
	}
	return results
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	encoded := hex.EncodeToString(b[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
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

func output(title, subtitle, text string) toolOutput {
	runes := []rune(text)
	if len(runes) > maxOutput {
		text = string(runes[:maxOutput]) + fmt.Sprintf("…（已截断，共 %d 字符）", len(runes))
	}
	return toolOutput{Title: title, Subtitle: subtitle, Text: text}
}
