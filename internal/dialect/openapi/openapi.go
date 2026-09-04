// Package openapi translates OpenAPI 3.x and Swagger 2.0 into callable operations.
//
// The translator does not write a parser: `kin-openapi` already resolves `$ref` (nested and
// external included), reads JSON and YAML, and understands 3.0 and 3.1; Swagger 2.0 comes in
// converted to 3. Writing that by hand costs a partial model — and whatever the model does not
// cover arrives impoverished at the other end.
//
// Here each parameter's schema is handed over AS IT IS in the spec (enum, format, array items,
// nested objects): the reader is an LLM, and every field lost along the way is a call it has to
// guess at.
package openapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi2"
	"github.com/getkin/kin-openapi/openapi2conv"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/rosaldo/api-mcp/internal/auth"
	"github.com/rosaldo/api-mcp/internal/core"
	"github.com/rosaldo/api-mcp/internal/spec"
)

// Options are decisions made by whoever starts the server, not by the spec.
type Options struct {
	BaseURL      string // beats the spec's `servers`
	Auth         auth.Applier
	Client       *http.Client
	IncludePaths []*regexp.Regexp
	ExcludePaths []*regexp.Regexp
	// ExcludeOps casa contra `MÉTODO /caminho` — é o filtro que os outros quatro não conseguem
	// expressar, porque eles decidem por caminho OU por método, nunca pelos dois juntos.
	//
	// O caso que o pediu: um conector de leitura que precisa alcançar as escritas de UMA área.
	// A eToro tem 87 leituras espalhadas por toda a API e 85 escritas; deixar passar as de
	// `watchlists` e `price-alerts` sem deixar passar as de `trading` e `posts` exige olhar o par.
	// Por caminho perderia os GETs de trading, que são metade do valor do conector.
	ExcludeOps     []*regexp.Regexp
	IncludeMethods []string
	ExcludeMethods []string
	Headers        map[string]string // fixed headers on every call
}

// Operations translates the whole document.
func Operations(ctx context.Context, doc *spec.Document, o Options) ([]core.Operation, error) {
	t, err := load(ctx, doc)
	if err != nil {
		return nil, err
	}
	base := o.BaseURL
	if base == "" {
		base = baseFromSpec(t)
	}
	if base == "" {
		return nil, fmt.Errorf("the spec does not say where the API lives and no --base-url was given")
	}
	// `None` is normalised to nil on purpose: with no authentication configured, an
	// `Authorization` declared in the spec is exactly what the model needs to fill in.
	if _, isNone := o.Auth.(auth.None); isNone {
		o.Auth = nil
	}
	if o.Client == nil {
		o.Client = &http.Client{}
	}

	var ops []core.Operation
	used := map[string]int{}
	for _, path := range sortedPaths(t.Paths.Map()) {
		item := t.Paths.Value(path)
		if !pathAllowed(path, o.IncludePaths, o.ExcludePaths) {
			continue
		}
		for method, op := range item.Operations() {
			if !methodAllowed(method, o.IncludeMethods, o.ExcludeMethods) {
				continue
			}
			if !opAllowed(method, path, o.ExcludeOps) {
				continue
			}
			// O servidor pode ser DA OPERAÇÃO, e não do documento. O OpenAPI 3 permite `servers`
			// em três níveis, e há APIs que usam isso a sério: a EvoLink serve a geração em
			// `api.evolink.ai` e os arquivos em `files-api.evolink.ai`, no mesmo documento.
			// Ler só o do topo mandava as três rotas de arquivo para o host errado — 403, com a
			// resposta certa escrita na descrição da própria tool.
			ops = append(ops, build(strings.TrimSuffix(baseDaOperacao(base, o.BaseURL, op, item), "/"),
				path, method, op, item, o, used))
		}
	}
	if len(ops) == 0 {
		return nil, fmt.Errorf("the spec yielded no operations (too many filters, or empty `paths`)")
	}
	return ops, nil
}

// load hands back a 3.x document, wherever it came from.
func load(ctx context.Context, doc *spec.Document) (*openapi3.T, error) {
	loader := openapi3.NewLoader()
	loader.Context = ctx
	loader.IsExternalRefsAllowed = true

	// Swagger 2.0 is not read by the 3.x loader, so it gets converted first. The conversion is
	// kin-openapi's own, so the result is the same model as the rest of the path.
	if isSwagger2(doc.Raw) {
		var v2 openapi2.T
		if err := json.Unmarshal(toJSON(doc.Raw), &v2); err != nil {
			return nil, fmt.Errorf("reading Swagger 2.0: %w", err)
		}
		t, err := openapi2conv.ToV3(&v2)
		if err != nil {
			return nil, fmt.Errorf("converting Swagger 2.0 to OpenAPI 3: %w", err)
		}
		return t, nil
	}

	t, err := loader.LoadFromData(doc.Raw)
	if err != nil {
		return nil, fmt.Errorf("reading OpenAPI: %w", err)
	}
	return t, nil
}

func isSwagger2(raw []byte) bool {
	var top struct {
		Swagger string `json:"swagger"`
	}
	_ = json.Unmarshal(toJSON(raw), &top)
	return strings.HasPrefix(top.Swagger, "2.")
}

// toJSON accepts YAML and returns JSON; JSON passes through untouched. The openapi2 model only
// reads JSON, and the spec may well have arrived as YAML.
func toJSON(raw []byte) []byte {
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return raw
	}
	var anything any
	if err := yamlUnmarshal(raw, &anything); err != nil {
		return raw
	}
	b, err := json.Marshal(anything)
	if err != nil {
		return raw
	}
	return b
}

// baseDaOperacao escolhe onde ESTA operação vive: o `servers` dela, o do path, e por fim o do
// documento — a ordem que o OpenAPI 3 define, do mais específico ao mais geral.
//
// `--base-url` continua vencendo todos: quem o passou está dizendo, explicitamente, para onde
// mandar tudo — em geral um ambiente de teste ou um proxy —, e um override no documento não pode
// desviar parte do tráfego para fora dele.
func baseDaOperacao(base, flag string, op *openapi3.Operation, item *openapi3.PathItem) string {
	if flag != "" {
		return flag
	}
	// `op.Servers` é PONTEIRO para a lista (a lib distingue "sem servers" de "lista vazia"),
	// enquanto o do path item é a lista direta. A assimetria é dela, não nossa.
	if op != nil && op.Servers != nil && len(*op.Servers) > 0 && (*op.Servers)[0].URL != "" {
		return (*op.Servers)[0].URL
	}
	if item != nil && len(item.Servers) > 0 && item.Servers[0].URL != "" {
		return item.Servers[0].URL
	}
	return base
}

func baseFromSpec(t *openapi3.T) string {
	if len(t.Servers) == 0 {
		return ""
	}
	return t.Servers[0].URL
}

func sortedPaths(m map[string]*openapi3.PathItem) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	// Stable order: the tool list must not shuffle between two starts of the server, or two
	// identical containers would announce different catalogues.
	sort.Strings(names)
	return names
}

// opAllowed casa cada padrão contra `MÉTODO /caminho` — em maiúsculas, um espaço, como aparece
// numa linha de log. É o formato que quem escreve o filtro já tem na cabeça.
func opAllowed(method, path string, exclude []*regexp.Regexp) bool {
	if len(exclude) == 0 {
		return true
	}
	op := strings.ToUpper(method) + " " + path
	for _, re := range exclude {
		if re.MatchString(op) {
			return false
		}
	}
	return true
}

func pathAllowed(path string, include, exclude []*regexp.Regexp) bool {
	for _, re := range exclude {
		if re.MatchString(path) {
			return false
		}
	}
	if len(include) == 0 {
		return true
	}
	for _, re := range include {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}

func methodAllowed(method string, include, exclude []string) bool {
	m := strings.ToUpper(method)
	for _, e := range exclude {
		if strings.EqualFold(e, m) {
			return false
		}
	}
	if len(include) == 0 {
		return true
	}
	for _, i := range include {
		if strings.EqualFold(i, m) {
			return true
		}
	}
	return false
}
