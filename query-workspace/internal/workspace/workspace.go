package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"tpt-graph/query-workspace/internal/queryparams"
)

var ErrNotFound = errors.New("saved query not found")

type GeneratedQuery struct {
	Cypher      string                 `json:"cypher"`
	Parameters  map[string]any         `json:"parameters"`
	Diagnostics *GenerationDiagnostics `json:"-"`
}

type QueryResult struct {
	Columns   []string
	Rows      [][]string
	Truncated bool
	Graph     ResultGraph
}

// ResultGraph holds the distinct graph entities a query returned, keyed by element ID.
type ResultGraph struct {
	Nodes         []GraphNode
	Relationships []GraphRelationship
	// RowNodes lists, per collected row, the element IDs of nodes that row references.
	RowNodes [][]string
}

type GraphNode struct {
	ID         string
	Labels     []string
	Properties map[string]string
}

type GraphRelationship struct {
	ID, Type, StartID, EndID string
	Properties               map[string]string
}

// InputMode is the composer tab a run belongs to.
type InputMode string

const (
	ModeAsk    InputMode = "ask"
	ModeCypher InputMode = "cypher"
)

// RunSource records where the executed Cypher came from.
type RunSource string

const (
	SourceGenerated RunSource = "generated"
	SourceSaved     RunSource = "saved"
	SourceWritten   RunSource = "written"
)

// QueryKind distinguishes saved questions from saved hand-written Cypher.
type QueryKind string

const (
	KindQuestion QueryKind = "question"
	KindCypher   QueryKind = "cypher"
)

func (k QueryKind) mode() InputMode {
	if k == KindCypher {
		return ModeCypher
	}
	return ModeAsk
}

type SavedQuery struct {
	ID         string
	Name       string
	FolderPath string
	Kind       QueryKind
	Question   string
	Cypher     string
	Parameters map[string]any
	CreatedAt  time.Time
}

type Investigation struct {
	Source             RunSource
	Kind               QueryKind
	SavedName          string
	Question           string
	Query              GeneratedQuery
	Result             QueryResult
	GenerationDuration time.Duration
	ExecutionDuration  time.Duration
	StartedAt          time.Time
	Stage              runStage
	TimedOut           bool
	ParametersInvalid  bool
}

type runStage string

const (
	stageGenerate runStage = "generate"
	stageValidate runStage = "validate"
	stageExecute  runStage = "execute"
)

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
	investigation := Investigation{Source: SourceGenerated, Kind: KindQuestion, Question: question}
	if question == "" {
		return investigation, errors.New("enter an investigation question")
	}
	if len(question) > maxQuestionBytes {
		return investigation, errors.New("question must be 4000 characters or fewer")
	}

	started := time.Now()
	investigation.StartedAt, investigation.Stage = started, stageGenerate
	generated, err := s.generator.Generate(ctx, question)
	generationDuration := time.Since(started)
	investigation.Query = generated
	investigation.GenerationDuration = generationDuration
	if s.latencies != nil {
		s.latencies.ObserveGeneration(generationDuration)
	}
	if err != nil {
		var generationErr *GenerationError
		investigation.TimedOut = errors.Is(err, context.DeadlineExceeded) ||
			errors.As(err, &generationErr) && (generationErr.Kind == "upstream_timeout" || generationErr.Kind == "local_timeout")
		s.generationFailed()
		return investigation, fmt.Errorf("generating query: %w", err)
	}
	if strings.TrimSpace(generated.Cypher) == "" {
		s.generationFailed()
		return investigation, errors.New("model returned an empty query")
	}
	if len(generated.Cypher) > maxCypherBytes {
		s.generationFailed()
		return investigation, errors.New("generated query exceeds 100000 characters")
	}
	err = s.validateAndExecute(ctx, &investigation)
	if err != nil && investigation.Stage == stageValidate {
		s.generationFailed()
	}
	return investigation, err
}

// RunWritten validates and executes Cypher typed by the user. Generation is never called.
func (s *Service) RunWritten(ctx context.Context, cypher, parameters string) (Investigation, error) {
	investigation := Investigation{
		Source: SourceWritten, Kind: KindCypher, StartedAt: time.Now(), Stage: stageValidate,
		Query: GeneratedQuery{Cypher: strings.TrimSpace(cypher), Parameters: map[string]any{}},
	}
	if investigation.Query.Cypher == "" {
		return investigation, errors.New("validation: enter a Cypher query")
	}
	if len(investigation.Query.Cypher) > maxCypherBytes {
		return investigation, errors.New("validation: query exceeds 100000 characters")
	}
	if strings.TrimSpace(parameters) != "" {
		decoded, err := decodeParameters(parameters)
		if err != nil {
			investigation.ParametersInvalid = true
			return investigation, err
		}
		investigation.Query.Parameters = decoded
	}
	return investigation, s.validateAndExecute(ctx, &investigation)
}

func (s *Service) RunSaved(ctx context.Context, id string) (SavedQuery, Investigation, error) {
	query, err := s.catalog.Get(ctx, id)
	if err != nil {
		return SavedQuery{}, Investigation{}, err
	}
	question := query.Question
	if question == "" && query.Kind != KindCypher {
		question = query.Name
	}
	investigation := Investigation{
		Source: SourceSaved, Kind: query.Kind, SavedName: query.Name, StartedAt: time.Now(),
		Question: question, Query: GeneratedQuery{Cypher: query.Cypher, Parameters: query.Parameters},
	}
	if err := s.validateAndExecute(ctx, &investigation); err != nil {
		return query, investigation, err
	}
	return query, investigation, nil
}

// validateAndExecute is the single path from Cypher to results for every run source.
func (s *Service) validateAndExecute(ctx context.Context, investigation *Investigation) error {
	investigation.Stage = stageValidate
	if err := validateReadQuery(investigation.Query.Cypher); err != nil {
		return err
	}
	investigation.Stage = stageExecute
	started := time.Now()
	result, err := s.executor.Execute(ctx, investigation.Query.Cypher, investigation.Query.Parameters)
	investigation.ExecutionDuration = time.Since(started)
	investigation.Result = result
	if s.latencies != nil {
		s.latencies.ObserveExecution(investigation.ExecutionDuration)
		if err != nil {
			s.latencies.ExecutionFailed()
		}
	}
	if err != nil {
		return fmt.Errorf("executing query: %w", err)
	}
	return nil
}

func (s *Service) generationFailed() {
	if s.latencies != nil {
		s.latencies.GenerationFailed()
	}
}

func (s *Service) Save(ctx context.Context, name, folder string, investigation Investigation) (SavedQuery, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return SavedQuery{}, errors.New("query name is required")
	}
	if len(name) > 200 {
		return SavedQuery{}, errors.New("query name must be 200 characters or fewer")
	}
	folderPath, err := normalizeFolderPath(folder)
	if err != nil {
		return SavedQuery{}, err
	}
	kind := investigation.Kind
	if kind != KindCypher {
		kind = KindQuestion
	}
	question := strings.TrimSpace(investigation.Question)
	if kind == KindCypher {
		question = ""
	}
	if len(question) > maxQuestionBytes {
		return SavedQuery{}, errors.New("question must be 4000 characters or fewer")
	}
	cypher := strings.TrimSpace(investigation.Query.Cypher)
	if cypher == "" {
		return SavedQuery{}, errors.New("query text is required")
	}
	if len(cypher) > maxCypherBytes {
		return SavedQuery{}, errors.New("query text exceeds 100000 characters")
	}
	if err := validateReadQuery(cypher); err != nil {
		return SavedQuery{}, err
	}
	parameters := investigation.Query.Parameters
	if parameters == nil {
		parameters = map[string]any{}
	}
	encodedParameters, err := json.Marshal(parameters)
	if err != nil {
		return SavedQuery{}, fmt.Errorf("encoding query parameters: %w", err)
	}
	if len(encodedParameters) > maxCypherBytes {
		return SavedQuery{}, errors.New("query parameters exceed 100000 bytes")
	}
	return s.catalog.Save(ctx, SavedQuery{
		Name:       name,
		FolderPath: folderPath,
		Kind:       kind,
		Question:   question,
		Cypher:     cypher,
		Parameters: parameters,
	})
}

func (s *Service) List(ctx context.Context) ([]SavedQuery, error) {
	return s.catalog.List(ctx)
}

const (
	maxQuestionBytes = 4000
	maxCypherBytes   = 100_000
	defaultFolder    = "Drafts"
)

// normalizeFolderPath accepts › or > as separators, trims segments and drops empty ones.
func normalizeFolderPath(folder string) (string, error) {
	var segments []string
	for segment := range strings.SplitSeq(strings.ReplaceAll(folder, "›", ">"), ">") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		if len(segment) > 100 {
			return "", errors.New("each folder name must be 100 characters or fewer")
		}
		segments = append(segments, segment)
	}
	if len(segments) == 0 {
		return defaultFolder, nil
	}
	if len(segments) > 10 {
		return "", errors.New("folders can be nested at most 10 levels deep")
	}
	return strings.Join(segments, folderSeparator), nil
}

const folderSeparator = " > "

func decodeParameters(raw string) (map[string]any, error) {
	if len(raw) > maxCypherBytes {
		return nil, errors.New("validation: parameters exceed 100000 bytes")
	}
	parameters, err := queryparams.Decode([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("validation: parameters must be one JSON object: %w", err)
	}
	return parameters, nil
}

func validateReadQuery(query string) error {
	tokens, err := cypherTokens(query)
	if err != nil {
		return fmt.Errorf("validation: %w", err)
	}
	if len(tokens) == 0 {
		return errors.New("validation: the query is empty")
	}
	start := 0
	if tokens[0] == "OPTIONAL" && len(tokens) > 1 && tokens[1] == "MATCH" {
		start = 1
	}
	switch tokens[start] {
	case "MATCH", "UNWIND", "WITH", "RETURN":
	default:
		return errors.New("validation: a query must start with MATCH, OPTIONAL MATCH, UNWIND, WITH or RETURN")
	}
	for _, token := range tokens {
		switch token {
		case "CREATE", "MERGE", "SET", "REMOVE", "DELETE", "DETACH", "FOREACH":
			return fmt.Errorf("validation: write clause %s is not permitted", token)
		case "CALL":
			return errors.New("validation: procedure and subquery calls (CALL) are not permitted")
		case "DROP", "ALTER", "RENAME", "GRANT", "DENY", "REVOKE", "TERMINATE", "START", "STOP":
			return fmt.Errorf("validation: administration command %s is not permitted", token)
		case "LOAD", "USE":
			return fmt.Errorf("validation: clause %s is not permitted", token)
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
		case query[i] == '/' && i+1 < len(query) && query[i+1] == '*':
			return nil, errors.New("block comments are not permitted")
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
		case query[i] == '$':
			i++
			for i < len(query) && isWordPart(query[i]) {
				i++
			}
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
