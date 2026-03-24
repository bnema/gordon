package components

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// NOTE: stripANSI is already defined in table_test.go (same package).
// Use it directly — do NOT redefine it here.

func TestTree_SingleNodeNoChildren(t *testing.T) {
	tree := NewTree()
	tree.AddNode("my-app.dev", "myapp:latest")

	rendered := stripANSI(tree.Render())

	assert.Contains(t, rendered, "my-app.dev")
	assert.Contains(t, rendered, "myapp:latest")
}

func TestTree_NodeWithOneChild(t *testing.T) {
	tree := NewTree()
	node := tree.AddNode("my-app.dev", "myapp:latest")
	node.AddChild("my-postgres", "postgres:16")

	rendered := stripANSI(tree.Render())

	assert.Contains(t, rendered, "my-app.dev")
	assert.Contains(t, rendered, "myapp:latest")
	assert.Contains(t, rendered, "└─")
	assert.Contains(t, rendered, "my-postgres")
	assert.Contains(t, rendered, "postgres:16")
}

func TestTree_NodeWithMultipleChildren(t *testing.T) {
	tree := NewTree()
	node := tree.AddNode("my-app.dev", "myapp:latest")
	node.AddChild("my-postgres", "postgres:16")
	node.AddChild("my-redis", "redis:7")

	rendered := stripANSI(tree.Render())

	assert.Contains(t, rendered, "├─")
	assert.Contains(t, rendered, "└─")
	assert.Contains(t, rendered, "my-postgres")
	assert.Contains(t, rendered, "my-redis")
}

func TestTree_GroupWithNodes(t *testing.T) {
	tree := NewTree()
	group := tree.AddGroup("shared-services")
	group.AddNode("app-a.dev", "app-a:latest")
	group.AddNode("app-b.dev", "app-b:latest")

	rendered := stripANSI(tree.Render())

	assert.Contains(t, rendered, "shared-services")
	assert.Contains(t, rendered, "◆")
	assert.Contains(t, rendered, "│")
	assert.Contains(t, rendered, "app-a.dev")
	assert.Contains(t, rendered, "app-b.dev")
}

func TestTree_MixedNodesAndGroups(t *testing.T) {
	tree := NewTree()
	tree.AddNode("alpha.dev", "alpha:latest")

	group := tree.AddGroup("my-group")
	group.AddNode("beta.dev", "beta:latest")
	group.AddNode("gamma.dev", "gamma:latest")

	tree.AddNode("delta.dev", "delta:latest")

	rendered := stripANSI(tree.Render())

	assert.Contains(t, rendered, "alpha.dev")
	assert.Contains(t, rendered, "my-group")
	assert.Contains(t, rendered, "beta.dev")
	assert.Contains(t, rendered, "gamma.dev")
	assert.Contains(t, rendered, "delta.dev")
}

func TestTree_EmptyTreeRendersEmpty(t *testing.T) {
	tree := NewTree()
	rendered := tree.Render()
	assert.Empty(t, rendered)
}

func TestTree_NodeWithChildSubtitleIndented(t *testing.T) {
	tree := NewTree()
	node := tree.AddNode("my-app.dev", "myapp:latest")
	node.AddChild("my-postgres", "postgres:16")

	rendered := stripANSI(tree.Render())
	lines := strings.Split(rendered, "\n")

	var childSubtitleLine string
	for _, line := range lines {
		if strings.Contains(line, "postgres:16") {
			childSubtitleLine = line
			break
		}
	}
	require.NotEmpty(t, childSubtitleLine, "child subtitle line should exist")
}

func TestTree_GroupNodeWithChild(t *testing.T) {
	tree := NewTree()
	group := tree.AddGroup("shared")
	node := group.AddNode("app.dev", "app:latest")
	node.AddChild("app-db", "postgres:16")

	rendered := stripANSI(tree.Render())

	assert.Contains(t, rendered, "shared")
	assert.Contains(t, rendered, "app.dev")
	assert.Contains(t, rendered, "app-db")
	assert.Contains(t, rendered, "postgres:16")
}
