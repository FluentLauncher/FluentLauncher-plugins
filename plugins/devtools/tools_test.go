package main

import (
	"strings"
	"testing"
)

func TestHash(t *testing.T) {
	outputs := run("hash", "abc")
	if len(outputs) != 3 || !strings.Contains(outputs[0].Text, "ba7816bf8f01cfea") {
		t.Fatalf("hash = %+v", outputs)
	}
}

func TestJSON(t *testing.T) {
	outputs := run("json", `{"a":1,}`)
	if len(outputs) != 2 || !strings.Contains(outputs[0].Text, `"a": 1`) {
		t.Fatalf("json = %+v", outputs)
	}
}

func TestBase64(t *testing.T) {
	outputs := run("base64", "aGVsbG8=")
	if len(outputs) != 2 || outputs[0].Text != "hello" {
		t.Fatalf("base64 = %+v", outputs)
	}
}

func TestURL(t *testing.T) {
	outputs := run("urlencode", "a b")
	if len(outputs) == 0 || outputs[0].Text != "a%20b" {
		t.Fatalf("url = %+v", outputs)
	}
}

func TestRegex(t *testing.T) {
	outputs := run("regex", `\d+ abc123def456`)
	if len(outputs) != 1 || outputs[0].Text != "123 | 456" {
		t.Fatalf("regex matches = %+v", outputs)
	}
	outputs = run("regex", `(\w+)@(\w+) mail me at bob@store`)
	if len(outputs) != 2 || !strings.Contains(outputs[1].Text, "1: bob") || !strings.Contains(outputs[1].Text, "2: store") {
		t.Fatalf("regex groups = %+v", outputs)
	}
	if outputs := run("regex", `x nothing`); len(outputs) != 1 || outputs[0].Title != "无匹配" {
		t.Fatalf("regex no match = %+v", outputs)
	}
	if outputs := run("regex", `( bad`); len(outputs) != 1 || outputs[0].Title != "正则无效" {
		t.Fatalf("regex invalid = %+v", outputs)
	}
	if outputs := run("regex", ""); len(outputs) != 1 || outputs[0].Title != "用法" {
		t.Fatalf("regex usage = %+v", outputs)
	}
}

func TestColor(t *testing.T) {
	want := []string{"#3366AA", "#3366aa", "rgb(51, 102, 170)", "hsl(214, 54%, 43%)", "51, 102, 170", "0x3366AA"}
	for _, input := range []string{"#36a", "3366AA", "rgb(51, 102, 170)", "51 102 170", "rgb 51，102，170"} {
		outputs := run("color", input)
		if len(outputs) != len(want) {
			t.Fatalf("%s = %+v", input, outputs)
		}
		for i, text := range want {
			if outputs[i].Text != text {
				t.Fatalf("%s row %d = %q, want %q", input, i, outputs[i].Text, text)
			}
		}
	}
	// HSL comes back to the same RGB, within rounding.
	if outputs := run("color", "hsl(214, 54%, 43%)"); len(outputs) == 0 || outputs[0].Text != "#3266A9" {
		t.Fatalf("hsl = %+v", outputs)
	}
	for _, input := range []string{"rgb(300, 0, 0)", "#12345", "red", ""} {
		if outputs := run("color", input); len(outputs) != 1 || outputs[0].Text != "" {
			t.Fatalf("%q = %+v", input, outputs)
		}
	}
}

// Time and UUIDs belong to the launcher's built-in tools; their keywords
// would never reach the plugin.
func TestTimeAndUUIDAreGone(t *testing.T) {
	for _, keyword := range []string{"time", "uuid"} {
		if outputs := run(keyword, ""); len(outputs) != 1 || outputs[0].Title != "未知工具" {
			t.Fatalf("%s = %+v", keyword, outputs)
		}
	}
}
