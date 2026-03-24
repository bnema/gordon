package components

import (
	"strings"

	"github.com/bnema/gordon/internal/adapters/in/cli/ui/styles"
)

// Node represents a tree node with a title, optional subtitle, and children.
type Node struct {
	Title    string
	Subtitle string
	Children []*Node
}

// AddChild adds a child node under this node.
func (n *Node) AddChild(title, subtitle string) *Node {
	child := &Node{Title: title, Subtitle: subtitle}
	n.Children = append(n.Children, child)
	return child
}

// Group represents a named group of nodes rendered with a vertical bar connector.
type Group struct {
	Name  string
	Nodes []*Node
}

// AddNode adds a node inside this group.
func (g *Group) AddNode(title, subtitle string) *Node {
	node := &Node{Title: title, Subtitle: subtitle}
	g.Nodes = append(g.Nodes, node)
	return node
}

// treeItem is a union type for ordering nodes and groups.
type treeItem struct {
	node  *Node
	group *Group
}

// Tree is a renderable tree structure for terminal output.
type Tree struct {
	items []treeItem
}

// NewTree creates a new empty tree.
func NewTree() *Tree {
	return &Tree{}
}

// AddNode adds a top-level node with a pre-styled title and a plain subtitle.
func (t *Tree) AddNode(title, subtitle string) *Node {
	node := &Node{Title: title, Subtitle: subtitle}
	t.items = append(t.items, treeItem{node: node})
	return node
}

// AddGroup adds a named group header.
func (t *Tree) AddGroup(name string) *Group {
	group := &Group{Name: name}
	t.items = append(t.items, treeItem{group: group})
	return group
}

// Render returns the full styled string output.
func (t *Tree) Render() string {
	if len(t.items) == 0 {
		return ""
	}

	var b strings.Builder
	for i, item := range t.items {
		if i > 0 {
			b.WriteString("\n")
		}
		if item.group != nil {
			t.renderGroup(&b, item.group)
		} else {
			t.renderNode(&b, item.node, "  ")
		}
	}
	return b.String()
}

func (t *Tree) renderGroup(b *strings.Builder, group *Group) {
	// Group header: ◆ group-name
	icon := styles.Theme.Highlight.Render(styles.IconGroup)
	name := styles.Theme.Bold.Render(group.Name)
	b.WriteString("  " + icon + " " + name + "\n")

	if len(group.Nodes) == 0 {
		return
	}

	vertBar := styles.Theme.Muted.Render(styles.IconTreeVert)
	b.WriteString("  " + vertBar + "\n")

	for _, node := range group.Nodes {
		t.renderNode(b, node, "  "+vertBar+"  ")
	}
}

func (t *Tree) renderNode(b *strings.Builder, node *Node, prefix string) {
	// Title line
	b.WriteString(prefix + node.Title + "\n")

	// Subtitle line (muted, aligned with title text after icons)
	if node.Subtitle != "" {
		subtitle := styles.Theme.Muted.Render(node.Subtitle)
		b.WriteString(prefix + "     " + subtitle + "\n")
	}

	// Children
	for i, child := range node.Children {
		isLast := i == len(node.Children)-1

		var branch string
		var childPrefix string
		if isLast {
			branch = styles.Theme.Muted.Render(styles.IconTreeLast + styles.IconTreeLine)
			childPrefix = "  "
		} else {
			branch = styles.Theme.Muted.Render(styles.IconTreeBranch + styles.IconTreeLine)
			childPrefix = styles.Theme.Muted.Render(styles.IconTreeVert) + " "
		}

		// Child title
		b.WriteString(prefix + "  " + branch + " " + child.Title + "\n")

		// Child subtitle (indented to align under child name)
		if child.Subtitle != "" {
			subtitle := styles.Theme.Muted.Render(child.Subtitle)
			b.WriteString(prefix + "  " + childPrefix + "   " + subtitle + "\n")
		}
	}
}
