package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ugurcan-aytar/anvil/internal/engine"
	"github.com/ugurcan-aytar/anvil/internal/wiki"
)

// graphOptions are the `anvil graph` flags.
type graphOptions struct {
	Output string
	NoOpen bool
}

var graphOpts graphOptions

var graphCmd = &cobra.Command{
	Use:   "graph",
	Short: "Export wiki as an interactive d3 force-directed HTML graph",
	Long: `anvil graph builds a single-file HTML visualisation of the wiki's
cross-reference graph. Nodes are coloured by page type (concept /
entity / source / synthesis), sized by inbound link count, and
outlined red when orphaned. Missing-page references render as grey
dashed nodes so the gap is visible.

Without --output the file is written to ./anvil-graph.html and
opened in the default browser. Pass --no-open to suppress that.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runGraph(cmd.Context(), graphOpts)
	},
}

func init() {
	graphCmd.Flags().StringVarP(&graphOpts.Output, "output", "o", "",
		"output HTML path (default: ./anvil-graph.html)")
	graphCmd.Flags().BoolVar(&graphOpts.NoOpen, "no-open", false,
		"don't open the file in a browser after writing")
}

// graphNode is one d3 node. JSON tags keep the blob identical to what
// a hand-written d3 example would embed.
type graphNode struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Backlinks int    `json:"backlinks"`
	Orphan    bool   `json:"orphan"`
	Missing   bool   `json:"missing"`
}

// graphLink is one d3 edge.
type graphLink struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

// graphData is the blob embedded into the HTML — d3 reads it
// directly, no JSON.parse round-trip needed.
type graphData struct {
	Nodes []graphNode `json:"nodes"`
	Links []graphLink `json:"links"`
}

func runGraph(ctx context.Context, opts graphOptions) error {
	_ = ctx
	eng, err := engine.Open(projectDir)
	if err != nil {
		return err
	}
	defer eng.Close()

	pages, err := wiki.ListPages(eng.WikiDir())
	if err != nil {
		return fmt.Errorf("list pages: %w", err)
	}
	g, err := wiki.BuildGraph(eng.WikiDir())
	if err != nil {
		return fmt.Errorf("build graph: %w", err)
	}

	data := collectGraphData(pages, g)

	outPath := opts.Output
	if outPath == "" {
		outPath = "./anvil-graph.html"
	}
	if err := writeGraphHTML(outPath, filepath.Base(eng.ProjectRoot()), data); err != nil {
		return err
	}

	fmt.Printf("Graph written to %s (%d nodes, %d links)\n", outPath, len(data.Nodes), len(data.Links))
	if !opts.NoOpen {
		if err := openInBrowser(outPath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: couldn't open browser: %v\n", err)
		}
	}
	return nil
}

// collectGraphData turns pages + graph into the node/link slices
// d3 expects. A missing-page target adds a grey dashed node whose
// type is empty — the template picks that up for styling.
func collectGraphData(pages []*wiki.Page, g *wiki.Graph) *graphData {
	data := &graphData{}
	orphans := map[string]struct{}{}
	for _, o := range g.Orphans() {
		orphans[o] = struct{}{}
	}
	existing := map[string]struct{}{}
	for _, p := range pages {
		stem := strings.TrimSuffix(p.Filename, ".md")
		existing[stem] = struct{}{}
		_, isOrphan := orphans[stem]
		data.Nodes = append(data.Nodes, graphNode{
			ID:        stem,
			Type:      p.Type,
			Backlinks: len(g.Backlinks(stem)),
			Orphan:    isOrphan,
		})
	}
	for _, missing := range g.MissingPages() {
		data.Nodes = append(data.Nodes, graphNode{
			ID:      missing,
			Missing: true,
		})
	}
	// Edges — iterate pages again so we catch every outgoing link.
	for _, p := range pages {
		src := strings.TrimSuffix(p.Filename, ".md")
		for _, target := range wiki.ExtractWikilinks(p.Body) {
			tgt := strings.TrimSuffix(target, ".md")
			data.Links = append(data.Links, graphLink{Source: src, Target: tgt})
		}
	}
	return data
}

// writeGraphHTML serialises data into graphTemplateHTML and writes
// the result to path. The template embeds a JSON blob so the file
// works offline (only d3 itself comes from a CDN).
func writeGraphHTML(path, title string, data *graphData) error {
	blob, err := json.Marshal(data)
	if err != nil {
		return err
	}
	html := strings.ReplaceAll(graphTemplateHTML, "__TITLE__", title)
	html = strings.ReplaceAll(html, "__DATA__", string(blob))
	return os.WriteFile(path, []byte(html), 0o644)
}

// openInBrowser pokes the OS at the file. Best-effort; any error
// surfaces as a warning rather than a hard failure.
func openInBrowser(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", abs)
	case "linux":
		cmd = exec.Command("xdg-open", abs)
	case "windows":
		cmd = exec.Command("cmd", "/C", "start", abs)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}

// graphTemplateHTML is the single-file d3 force-directed graph.
// Two placeholders (__TITLE__, __DATA__) get substituted at write
// time. d3 loads from unpkg — the only network dependency. Kept as
// a raw string so the template stays readable during code review.
const graphTemplateHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>__TITLE__ · anvil graph</title>
<style>
  body { margin: 0; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif; background: #fafafa; }
  h1 { font-size: 1em; padding: 0.5em 1em; margin: 0; border-bottom: 1px solid #ddd; background: #fff; }
  svg { display: block; }
  .node circle { stroke: #fff; stroke-width: 1.5px; }
  .node.orphan circle { stroke: #cb2431; stroke-width: 2.5px; }
  .node.missing circle { fill: #ccc; stroke: #888; stroke-dasharray: 3,2; }
  .node text { font-size: 11px; fill: #333; pointer-events: none; }
  .link { stroke: #999; stroke-opacity: 0.55; }
  .link.to-missing { stroke-dasharray: 3,2; stroke-opacity: 0.4; }
  .legend { position: absolute; top: 60px; right: 1em; background: #fff; padding: 0.6em 0.9em; border: 1px solid #ddd; border-radius: 4px; font-size: 0.85em; }
  .legend .sw { display: inline-block; width: 12px; height: 12px; border-radius: 50%; vertical-align: middle; margin-right: 0.5em; }
</style>
</head>
<body>
<h1>__TITLE__ — wiki graph</h1>
<div class="legend">
  <div><span class="sw" style="background:#4c78a8"></span>concept</div>
  <div><span class="sw" style="background:#59a14f"></span>entity</div>
  <div><span class="sw" style="background:#edc948"></span>source</div>
  <div><span class="sw" style="background:#b279a2"></span>synthesis</div>
  <div><span class="sw" style="background:#bbb;border:1px dashed #888"></span>missing</div>
  <div><span class="sw" style="background:#fff;border:2px solid #cb2431"></span>orphan</div>
</div>
<svg id="graph"></svg>
<script src="https://unpkg.com/d3@7/dist/d3.min.js"></script>
<script>
const data = __DATA__;

const typeColor = { concept: "#4c78a8", entity: "#59a14f", source: "#edc948", synthesis: "#b279a2" };
const nodeColor = d => d.missing ? "#ccc" : (typeColor[d.type] || "#999");
const nodeSize = d => d.missing ? 5 : 6 + Math.min(d.backlinks, 12) * 1.2;

const width = window.innerWidth;
const height = window.innerHeight - 60;
const svg = d3.select("#graph").attr("width", width).attr("height", height);

const ids = new Set(data.nodes.map(n => n.id));
data.links = data.links.filter(l => ids.has(l.source) && ids.has(l.target));

const sim = d3.forceSimulation(data.nodes)
  .force("link", d3.forceLink(data.links).id(d => d.id).distance(80))
  .force("charge", d3.forceManyBody().strength(-140))
  .force("center", d3.forceCenter(width / 2, height / 2))
  .force("collision", d3.forceCollide().radius(d => nodeSize(d) + 4));

const linkById = new Map();
data.nodes.forEach(n => linkById.set(n.id, n));

const link = svg.append("g").selectAll("line").data(data.links).enter().append("line")
  .attr("class", d => "link" + (linkById.get(typeof d.target === "string" ? d.target : d.target.id)?.missing ? " to-missing" : ""));

const node = svg.append("g").selectAll("g").data(data.nodes).enter().append("g")
  .attr("class", d => "node" + (d.orphan ? " orphan" : "") + (d.missing ? " missing" : ""))
  .call(d3.drag()
    .on("start", (e, d) => { if (!e.active) sim.alphaTarget(0.3).restart(); d.fx = d.x; d.fy = d.y; })
    .on("drag",  (e, d) => { d.fx = e.x; d.fy = e.y; })
    .on("end",   (e, d) => { if (!e.active) sim.alphaTarget(0); d.fx = null; d.fy = null; }));

node.append("circle").attr("r", nodeSize).attr("fill", nodeColor);
node.append("title").text(d => d.id + (d.missing ? " (missing)" : d.orphan ? " (orphan)" : ""));
node.append("text").attr("dx", d => nodeSize(d) + 4).attr("dy", "0.32em").text(d => d.id);

sim.on("tick", () => {
  link.attr("x1", d => d.source.x).attr("y1", d => d.source.y)
      .attr("x2", d => d.target.x).attr("y2", d => d.target.y);
  node.attr("transform", d => 'translate(' + d.x + ',' + d.y + ')');
});
</script>
</body>
</html>
`
