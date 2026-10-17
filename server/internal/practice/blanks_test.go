package practice

import "testing"

func TestCountBlanks(t *testing.T) {
	cases := []struct {
		name, in string
		want     int
	}{
		{"none", "没有填空 $x$ 的题", 0},
		{"one", "答案是 ________ 。", 1},
		{"exactly four", "____", 1},
		{"three is too short", "___ x", 0},
		{"fullwidth", "＿＿＿＿＿ 和 ＿＿＿＿", 2},
		{"mixed run is one blank", "_＿_＿_", 1},
		{"several", "a ________ b ________ c ____ d", 3},
		{"separated by a letter", "____x____", 2},
		{"underscores inside inline math are not blanks", "$x_____y$ 与 ________", 1},
		{"inside display math", "$$a_____b$$", 0},
		{"inside \\( \\)", `\(a_____b\) ________`, 1},
		{"inside \\[ \\]", `\[a_____b\]`, 0},
		{"escaped dollar is not math", `价格 \$5 与 \$6 ________`, 1},
		{"unmatched dollar is plain text", "只有一个 $ 符号 ________", 1},
		{"unmatched $$", "开头 $$ 没有结尾 ________", 1},
		{"blank right after math", "$x=$________", 1},
		{"blank between maths", "$a$ ____ $b$ ____", 2},
		{"newline separated", "____\n____", 2},
		{"long run is one blank", "________________", 1},
		{"underscore variable in prose", "a_b_c", 0},
		{"empty", "", 0},
	}
	for _, c := range cases {
		if got := CountBlanks(c.in); got != c.want {
			t.Errorf("%s: CountBlanks(%q)=%d want %d", c.name, c.in, got, c.want)
		}
	}
}

func TestSegments(t *testing.T) {
	type s = Segment
	cases := []struct {
		in   string
		want []Segment
	}{
		{"a $x$ b", []s{{"a ", false}, {"$x$", true}, {" b", false}}},
		{"$$x$$", []s{{"$$x$$", true}}},
		{`\(a\)\[b\]`, []s{{`\(a\)`, true}, {`\[b\]`, true}}},
		{`\$5 $x$`, []s{{`\$5 `, false}, {"$x$", true}}},
		{"$a\\$b$", []s{{`$a\$b$`, true}}},
		{"$$", []s{{"$$", false}}},
		{"$ unmatched", []s{{"$ unmatched", false}}},
		{"$$ unmatched", []s{{"$$ unmatched", false}}},
		{`\(unmatched`, []s{{`\(unmatched`, false}}},
		{"a$$b$$c", []s{{"a", false}, {"$$b$$", true}, {"c", false}}},
		{`$\begin{cases}x\\y\end{cases}$`, []s{{`$\begin{cases}x\\y\end{cases}$`, true}}},
		{"$|x|<2$", []s{{"$|x|<2$", true}}},
		{"", nil},
	}
	for _, c := range cases {
		got := Segments(c.in)
		if len(got) != len(c.want) {
			t.Errorf("%q: got %v want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%q[%d]: got %v want %v", c.in, i, got[i], c.want[i])
			}
		}
	}
}
