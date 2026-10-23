package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrNotFound = errors.New("saved query not found")

type GeneratedQuery struct {
	Cypher     string         `json:"cypher"`
	Parameters map[string]any `json:"parameters"`
}

type QueryResult struct {
	Columns   []string
	Rows      [][]string
	Truncated bool
}

type SavedQuery struct {
	ID         string
	Name       string
	Category   string
	Cypher     string
	Parameters map[string]any
	CreatedAt  time.Time
}

type Investigation struct {
	Question           string
	Query              GeneratedQuery
	Result             QueryResult
	GenerationDuration time.Duration
	ExecutionDuration  time.Duration
}

type Generator interface {
	Generate(context.Context, string) (GeneratedQuery, error)
}

type Executor interface {
	Execute(context.Context, string, map[string]any) (QueryResult, error)
}

type Catalog interface {
	Save(context.Context, SavedQuery) (SavedQuery, error)
	Get(context.Context, string) (SavedQuery, error)
	List(context.Context) ([]SavedQuery, error)
}

type LatencyRecorder interface {
	ObserveGeneration(time.Duration)
	ObserveExecution(time.Duration)
	GenerationFailed()
	ExecutionFailed()
}

type Service struct {
	generator Generator
	executor  Executor
	catalog   Catalog
	latencies LatencyRecorder
}

func NewService(generator Generator, executor Executor, catalog Catalog) *Service {
	return &Service{generator: generator, executor: executor, catalog: catalog}
}

func NewServiceWithMetrics(generator Generator, executor Executor, catalog Catalog, latencies LatencyRecorder) *Service {
	service := NewService(generator, executor, catalog)
	service.latencies = latencies
	return service
}

func (s *Service) Ask(ctx context.Context, question string) (Investigation, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return Investigation{}, errors.New("enter an investigation question")
	}
	if len(question) > 4000 {
		return Investigation{}, errors.New("question must be 4000 characters or fewer")
	}

	started := time.Now()
	generated, err := s.generator.Generate(ctx, question)
	generationDuration := time.Since(started)
	if s.latencies != nil {
		s.latencies.ObserveGeneration(generationDuration)
	}
	if err != nil {
		if s.latencies != nil {
			s.latencies.GenerationFailed()
		}
		return Investigation{Question: question, GenerationDuration: generationDuration}, fmt.Errorf("generating query: %w", err)
	}
	if strings.TrimSpace(generated.Cypher) == "" {
		if s.latencies != nil {
			s.latencies.GenerationFailed()
		}
		return Investigation{Question: question, GenerationDuration: generationDuration}, errors.New("model returned an empty query")
	}
	if len(generated.Cypher) > 100_000 {
		if s.latencies != nil {
			s.latencies.GenerationFailed()
		}
		return Investigation{Question: question, GenerationDuration: generationDuration}, errors.New("generated query exceeds 100000 characters")
	}
	if err := validateReadQuery(generated.Cypher); err != nil {
		if s.latencies != nil {
			s.latencies.GenerationFailed()
		}
		return Investigation{Question: question, Query: generated, GenerationDuration: generationDuration}, err
	}

	investigation := Investigation{Question: question, Query: generated, GenerationDuration: generationDuration}
	started = time.Now()
	result, err := s.executor.Execute(ctx, generated.Cypher, generated.Parameters)
	investigation.ExecutionDuration = time.Since(started)
	if s.latencies != nil {
		s.latencies.ObserveExecution(investigation.ExecutionDuration)
	}
	investigation.Result = result
	if err != nil {
		if s.latencies != nil {
			s.latencies.ExecutionFailed()
		}
		return investigation, fmt.Errorf("executing query: %w", err)
	}
	return investigation, nil
}

func (s *Service) Save(ctx context.Context, name, category, cypher string, parameters map[string]any) (SavedQuery, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return SavedQuery{}, errors.New("query name is required")
	}
	if len(name) > 200 {
		return SavedQuery{}, errors.New("query name must be 200 characters or fewer")
	}
	category = strings.TrimSpace(category)
	if category == "" {
		category = "Draft"
	}
	if len(category) > 100 {
		return SavedQuery{}, errors.New("query category must be 100 characters or fewer")
	}
	cypher = strings.TrimSpace(cypher)
	if cypher == "" {
		return SavedQuery{}, errors.New("query text is required")
	}
	if len(cypher) > 100_000 {
		return SavedQuery{}, errors.New("query text exceeds 100000 characters")
	}
	if err := validateReadQuery(cypher); err != nil {
		return SavedQuery{}, err
	}
	if parameters == nil {
		parameters = map[string]any{}
	}
	encodedParameters, err := json.Marshal(parameters)
	if err != nil {
		return SavedQuery{}, fmt.Errorf("encoding query parameters: %w", err)
	}
	if len(encodedParameters) > 100_000 {
		return SavedQuery{}, errors.New("query parameters exceed 100000 bytes")
	}
	return s.catalog.Save(ctx, SavedQuery{
		Name:       name,
		Category:   category,
		Cypher:     cypher,
		Parameters: parameters,
	})
}

func (s *Service) List(ctx context.Context) ([]SavedQuery, error) {
	return s.catalog.List(ctx)
}

func (s *Service) RunSaved(ctx context.Context, id string) (SavedQuery, QueryResult, error) {
	query, err := s.catalog.Get(ctx, id)
	if err != nil {
		return SavedQuery{}, QueryResult{}, err
	}
	if err := validateReadQuery(query.Cypher); err != nil {
		return query, QueryResult{}, err
	}
	started := time.Now()
	result, err := s.executor.Execute(ctx, query.Cypher, query.Parameters)
	if s.latencies != nil {
		s.latencies.ObserveExecution(time.Since(started))
		if err != nil {
			s.latencies.ExecutionFailed()
		}
	}
	if err != nil {
		return query, result, fmt.Errorf("executing saved query: %w", err)
	}
	return query, result, nil
}

func validateReadQuery(query string) error {
	tokens, err := cypherTokens(query)
	if err != nil || len(tokens) == 0 {
		return errors.New("query is not a permitted read-only Cypher query")
	}
	start := 0
	if tokens[0] == "OPTIONAL" {
		if len(tokens) < 2 || tokens[1] != "MATCH" {
			return errors.New("query is not a permitted read-only Cypher query")
		}
		start = 1
	}
	switch tokens[start] {
	case "MATCH", "UNWIND", "WITH", "RETURN":
	default:
		return errors.New("query is not a permitted read-only Cypher query")
	}
	for _, token := range tokens {
		switch token {
		case "CREATE", "MERGE", "SET", "REMOVE", "DELETE", "DETACH",
			"DROP", "ALTER", "RENAME", "GRANT", "DENY", "REVOKE",
			"TERMINATE", "START", "STOP", "LOAD", "USE", "CALL", "FOREACH":
			return errors.New("query is not a permitted read-only Cypher query")
		}
	}
	return nil
}

func cypherTokens(query string) ([]string, error) {
	var tokens []string
	for i := 0; i < len(query); {
		switch {
		case isSpace(query[i]):
			i++
		case query[i] == '/' && i+1 < len(query) && query[i+1] == '/':
			i += 2
			for i < len(query) && query[i] != '\n' {
				i++
			}
		case query[i] == '\'' || query[i] == '"':
			quote := query[i]
			i++
			closed := false
			for i < len(query) {
				if query[i] == '\\' {
					i += 2
					continue
				}
				if query[i] == quote {
					if i+1 < len(query) && query[i+1] == quote {
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, errors.New("unterminated string literal")
			}
		case query[i] == '`':
			i++
			closed := false
			for i < len(query) {
				if query[i] == '`' {
					if i+1 < len(query) && query[i+1] == '`' {
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, errors.New("unterminated escaped identifier")
			}
		case query[i] == ';':
			return nil, errors.New("multiple Cypher statements are not permitted")
		case isWordStart(query[i]):
			start := i
			for i < len(query) && isWordPart(query[i]) {
				i++
			}
			tokens = append(tokens, strings.ToUpper(query[start:i]))
		default:
			i++
		}
	}
	return tokens, nil
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func isWordStart(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

func isWordPart(b byte) bool {
	return isWordStart(b) || b >= '0' && b <= '9'
}
