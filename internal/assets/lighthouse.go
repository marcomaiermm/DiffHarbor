//go:build ignore

// Writes reviewing{,-light,-dark}.svg here and into docs/public: an ASCII lighthouse whose beam sweeps round.
// Run by `go generate ./internal/assets`.
//
// Colors are Harbor Night tokens, inlined because an <img> cannot read the page's CSS: dark is the design
// board's Night value, light is theme.html's light value of the same token.
package main

import (
	"fmt"
	"html"
	"log"
	"math"
	"os"
	"strings"
)

const (
	cols, n, period = 66, 24, 3.0
	lampRow, c      = 3, cols / 2
	reach           = 29
	ramp            = " .:-=+*"
	cw, lh, pad     = 8.4, 16, 8
)

var tower = []string{
	"         ",
	"    _    ",
	"   /^\\   ",
	"  |[ ]|  ",
	" [=====] ",
	"  |   |  ",
	"  |###|  ",
	"  |   |  ",
	"  |###|  ",
	"  |   |  ",
	" /     \\ ",
}

var (
	left = c - len(tower[0])/2
	rows = len(tower) + 2 // two rows of sea
)

type cell struct{ ch, class string }

type point struct{ r, x int }

func frame(i int) [][]cell {
	grid := make([][]cell, rows)
	for r := range grid {
		grid[r] = make([]cell, cols)
		for x := range grid[r] {
			grid[r][x] = cell{" ", ""}
		}
	}
	for r, line := range tower {
		for k, ch := range line {
			if ch != ' ' {
				grid[r][left+k] = cell{string(ch), ""} // the tower is the text's own fill
			}
		}
	}
	for _, r := range []int{rows - 2, rows - 1} { // waves, the lower row drifting the other way
		s, class := i/3, "w" // 8 columns a cycle: one wave pattern, no seam
		if r == rows-1 {
			s, class = -s, "v" // v: the nearer row, dimmed
		}
		for x := range cols {
			grid[r][x] = cell{string("~-~^~--~"[((x+s)%8+8)%8]), class}
		}
	}
	for k, ch := range ` /_______\ ` { // the foot stands in the water
		if ch != ' ' {
			grid[rows-2][left-1+k] = cell{string(ch), ""}
		}
	}

	light := map[point]float64{}
	theta := 2 * math.Pi * float64(i) / n
	for age, weight := range []float64{1, 0.6, 0.35, 0.18} { // the beam, then its trail
		a := theta - float64(age)*0.22
		dx := math.Cos(a)
		length := reach * math.Abs(dx)
		side := -1
		if dx > 0 {
			side = 1
		}
		start := c + side*3
		for d := 0; d <= int(length); d++ {
			x := start + side*d
			up, down := d/7, d/12
			for r := lampRow - up; r <= lampRow+down; r++ {
				edge := 1 - 0.35*math.Abs(float64(r-lampRow))/float64(up+1)
				v := weight * edge * (1 - 0.75*float64(d)/(reach+1))
				light[point{r, x}] = max(light[point{r, x}], v)
			}
		}
	}
	for p, v := range light {
		if p.r >= 0 && p.r < rows && p.x >= 0 && p.x < cols && grid[p.r][p.x].ch == " " {
			if lvl := min(len(ramp)-1, int(v*float64(len(ramp)))); lvl > 0 {
				grid[p.r][p.x] = cell{ramp[lvl : lvl+1], fmt.Sprintf("b%d", lvl)}
			}
		}
	}
	for x := range cols { // the beam glints on the crests below it
		if ch := grid[rows-2][x].ch; strings.Contains("^~", ch) && light[point{lampRow, x}] > 0.45 {
			grid[rows-2][x] = cell{ch, "g"}
		}
	}
	s := math.Sin(theta)
	if s > 0.6 { // facing us: rays round the lamp
		for _, ray := range []struct {
			dr          int
			left, right string
		}{{-1, `\`, "/"}, {0, "-", "-"}, {1, "/", `\`}} {
			grid[lampRow+ray.dr][c-4] = cell{ray.left, "l"}
			grid[lampRow+ray.dr][c+4] = cell{ray.right, "l"}
		}
	}
	switch {
	case s > 0.3:
		grid[lampRow][c] = cell{"@", "l"}
	case s > -0.5:
		grid[lampRow][c] = cell{"*", "l"}
	default:
		grid[lampRow][c] = cell{"o", ""}
	}
	return grid
}

// rowSVG is one row of a frame, a <tspan> per run of one class.
func rowSVG(cells []cell, y int) string {
	var out strings.Builder
	run, class := "", "\x00"
	flush := func() {
		if run == "" {
			return
		}
		if class == "" {
			out.WriteString(html.EscapeString(run))
		} else {
			fmt.Fprintf(&out, `<tspan class="%s">%s</tspan>`, class, html.EscapeString(run))
		}
	}
	for _, cl := range cells {
		if cl.class != class {
			flush()
			run = ""
		}
		run, class = run+cl.ch, cl.class
	}
	flush()
	return fmt.Sprintf(`<text y="%d" textLength="%v">%s</text>`, y, cols*cw, out.String())
}

// (ink3 tower, blue water, yellow lamp and beam): theme.html light --muted-foreground, --k-fn, --k-var;
// the board's Night --ink3, --blue, --yellow (theme.html dark has the same three)
var (
	lightInk = [3]string{"#6b6b6b", "#1f5fc4", "#8c6c3e"}
	nightInk = [3]string{"#98a1cc", "#7aa2f7", "#e0af68"}
)

func palette(ink [3]string) string {
	lit := []string{".l", ".g"}
	for k := 1; k < len(ramp); k++ {
		lit = append(lit, fmt.Sprintf(".b%d", k))
	}
	return fmt.Sprintf("text{fill:%s}.w,.v{fill:%s}%s{fill:%s}", ink[0], ink[1], strings.Join(lit, ","), ink[2])
}

func svg(colors string, frames []string) string {
	w, h := cols*cw+2*pad, float64(rows*lh+2*pad)
	lines := []string{
		fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%.0f" height="%.0f" viewBox="0 0 %.0f %.0f" role="img">`, w, h, w, h),
		"<title>A lighthouse sweeping its beam</title>",
		"<style>",
		"text{font:14px 'Geist Mono',ui-monospace,SFMono-Regular,Menlo,Consolas,'DejaVu Sans Mono',monospace;white-space:pre}",
		colors,
		".g{fill-opacity:.75}.v{fill-opacity:.55}",
	}
	for k := 1; k < len(ramp)-1; k++ {
		lines = append(lines, fmt.Sprintf(".b%d{fill-opacity:%.2f}", k, 0.25+0.75*float64(k)/float64(len(ramp)-1)))
	}
	lines = append(lines,
		fmt.Sprintf(".f{opacity:0;animation:f %.1fs steps(1,end) infinite}", period),
		fmt.Sprintf("@keyframes f{0%%{opacity:1}%.3f%%,100%%{opacity:0}}", 100.0/n))
	for i := 1; i < n; i++ {
		lines = append(lines, fmt.Sprintf(".f%d{animation-delay:%.3fs}", i, float64(i)*period/n))
	}
	lines = append(lines,
		"@media (prefers-reduced-motion:reduce){.f{animation:none}.f0{opacity:1}}",
		"</style>",
		fmt.Sprintf(`<g transform="translate(%d %d)" xml:space="preserve">`, pad, pad+12))
	lines = append(lines, frames...)
	lines = append(lines, "</g>", "</svg>")
	return strings.Join(lines, "\n") + "\n"
}

func main() {
	var frames []string
	for i := range n {
		frames = append(frames, fmt.Sprintf(`<g class="f f%d">`, i))
		for r, cells := range frame(i) {
			frames = append(frames, rowSVG(cells, r*lh))
		}
		frames = append(frames, "</g>")
	}
	// GitHub picks -light or -dark by its own theme setting (<picture>); the plain one follows the OS, for the rest.
	files := map[string]string{
		"reviewing":       palette(lightInk) + "\n@media (prefers-color-scheme:dark){" + palette(nightInk) + "}",
		"reviewing-light": palette(lightInk),
		"reviewing-dark":  palette(nightInk),
	}
	for _, dir := range []string{".", "../../docs/public"} {
		for name, colors := range files {
			if err := os.WriteFile(dir+"/"+name+".svg", []byte(svg(colors, frames)), 0o644); err != nil { //nolint:gosec // public images
				log.Fatal(err)
			}
		}
	}
}
