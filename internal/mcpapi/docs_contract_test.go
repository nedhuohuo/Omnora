package mcpapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"omnora/internal/aitoken"
)

// docsRoot locates the repository from the package directory so this contract
// test remains usable from `go test ./...` and from the documentation gate.
func docsRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func readContractDoc(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func TestDocumentationMatchesMCPContract(t *testing.T) {
	root := docsRoot(t)
	mcpGuide := readContractDoc(t, root, "docs/mcp/README.md")
	openapi := readContractDoc(t, root, "openapi/omnora.v1.yaml")
	security := readContractDoc(t, root, "docs/security/security-model.md")
	requirements := readContractDoc(t, root, "docs/requirements/product-requirements.md")
	acceptance := readContractDoc(t, root, "docs/verification/acceptance-criteria.md")

	allSpecs := append(OrdinaryToolSpecs(), HighRiskToolSpecs()...)
	if len(allSpecs) != 24 {
		t.Fatalf("catalog must contain exactly 24 tools, got %d", len(allSpecs))
	}
	for _, spec := range allSpecs {
		if !strings.Contains(mcpGuide, "`"+spec.Name+"`") {
			t.Errorf("MCP guide is missing tool %q", spec.Name)
		}
	}
	seenScopes := map[aitoken.Scope]bool{}
	for _, spec := range allSpecs {
		seenScopes[spec.Scope] = true
	}
	if len(seenScopes) != 15 {
		t.Fatalf("catalog must contain exactly 15 scopes, got %d", len(seenScopes))
	}
	for scope := range seenScopes {
		literal := string(scope)
		if !strings.Contains(mcpGuide, literal) {
			t.Errorf("MCP guide is missing scope %q", literal)
		}
		if !strings.Contains(openapi, literal) {
			t.Errorf("OpenAPI is missing scope %q", literal)
		}
		if !strings.Contains(security, literal) {
			t.Errorf("security model is missing scope %q", literal)
		}
		if !strings.Contains(requirements, literal) {
			t.Errorf("product requirements are missing scope %q", literal)
		}
		if !strings.Contains(acceptance, literal) {
			t.Errorf("acceptance criteria are missing scope %q", literal)
		}
	}

	for _, path := range []string{"  /mcp:", "  /mcp/transfers/{publicId}:", "  /mcp/transfers/{publicId}/parts/{partNumber}:"} {
		if !strings.Contains(openapi, path) {
			t.Errorf("OpenAPI is missing path %q", path)
		}
	}
	for _, phrase := range []string{
		"2026-07-28",
		"2025-11-25",
		"NOT IMPLEMENTED",
		"MCP Wire Protocol：implemented and protocol-tested",
		"Omnora AI Token Authorization：implemented, non-OAuth",
		"MCP OAuth Authorization Profile：NOT IMPLEMENTED",
		"Streamable HTTP",
	} {
		if !strings.Contains(mcpGuide+security+requirements+acceptance, phrase) {
			t.Errorf("documentation contract is missing %q", phrase)
		}
	}
	for _, doc := range []struct {
		name string
		body string
	}{
		{"MCP guide", mcpGuide},
		{"REST guide", readContractDoc(t, root, "docs/api/README.md")},
		{"OpenAPI", openapi},
	} {
		if strings.Contains(doc.body, `{"method":"`) || strings.Contains(doc.body, "method-plus-params") || strings.Contains(doc.body, "required: [method]") {
			t.Errorf("%s still advertises obsolete private MCP envelope", doc.name)
		}
	}
}

func TestOpenAPIUsesAccountMountTokenContract(t *testing.T) {
	openapi := readContractDoc(t, docsRoot(t), "openapi/omnora.v1.yaml")

	for _, path := range []string{
		"  /member/content-sources:",
		"  /member/files/children:",
	} {
		if !strings.Contains(openapi, path) {
			t.Errorf("OpenAPI is missing member content path %q", path)
		}
	}

	tokenSchemas := contractSection(t, openapi, "    AiToken:\n", "    Job:\n")
	if !strings.Contains(tokenSchemas, `"mounts:read"`) {
		t.Error("AI Token scopes are missing mounts:read")
	}
	for _, obsolete := range []string{`"spaces:read"`, "spaceId:", "DirectoryBoundary"} {
		if strings.Contains(tokenSchemas, obsolete) {
			t.Errorf("AI Token schemas still contain obsolete Space contract %q", obsolete)
		}
	}
	for _, source := range []string{"all_account_content", "personal", "common_mount"} {
		if !strings.Contains(tokenSchemas, "const: "+source) {
			t.Errorf("AI Token boundary union is missing source %q", source)
		}
	}
	if strings.Contains(openapi, "\n    DirectoryBoundary:\n") {
		t.Error("OpenAPI still defines the obsolete DirectoryBoundary schema")
	}
}

func TestOpenAPIRemovesLegacySpaceBusinessContract(t *testing.T) {
	root := docsRoot(t)
	for _, name := range []string{
		"openapi/omnora.v1.yaml",
		"internal/server/openapi_assets/omnora.v1.yaml",
	} {
		body := readContractDoc(t, root, name)
		for _, obsolete := range []string{
			"  /spaces",
			"  /admin/spaces",
			"spaceId",
			"space_id",
			"SpaceId",
			"\n    Space:\n",
			"#/components/schemas/Space",
			"#/components/parameters/SpaceId",
		} {
			if strings.Contains(body, obsolete) {
				t.Errorf("%s still exposes obsolete Space business contract %q", name, obsolete)
			}
		}
		if strings.Contains(strings.ToLower(body), "space") {
			t.Errorf("%s still contains obsolete Space terminology", name)
		}
	}
}

func contractSection(t *testing.T, body, start, end string) string {
	t.Helper()
	startIndex := strings.Index(body, start)
	if startIndex < 0 {
		t.Fatalf("contract section start %q is missing", start)
	}
	endIndex := strings.Index(body[startIndex+len(start):], end)
	if endIndex < 0 {
		t.Fatalf("contract section end %q is missing", end)
	}
	return body[startIndex : startIndex+len(start)+endIndex]
}
