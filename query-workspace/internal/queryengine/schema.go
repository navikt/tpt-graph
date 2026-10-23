package queryengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	neodriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"tpt-graph/query-workspace/internal/schema"
)

const (
	nodePropertiesQuery         = "CALL db.schema.nodeTypeProperties()"
	relationshipPropertiesQuery = "CALL db.schema.relTypeProperties()"
	visualizationQuery          = "CALL db.schema.visualization()"
)

func (e *Neo4jExecutor) Discover(ctx context.Context) (text string, discoveryErr error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	session := e.driver.NewSession(ctx, neodriver.SessionConfig{
		AccessMode: neodriver.AccessModeRead, DatabaseName: e.database, FetchSize: 100,
	})
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer closeCancel()
		if err := session.Close(closeCtx); err != nil {
			discoveryErr = errors.Join(discoveryErr, fmt.Errorf("closing schema discovery session: %w", err))
		}
	}()
	return neodriver.ExecuteRead(ctx, session, func(tx neodriver.ManagedTransaction) (string, error) {
		return readNativeSchema(ctx, func(query string) (metadataRows, error) {
			return tx.Run(ctx, query, nil)
		})
	}, neodriver.WithTxTimeout(2*time.Minute))
}

type metadataRows interface {
	Next(context.Context) bool
	Record() *neodriver.Record
	Err() error
}

func readNativeSchema(ctx context.Context, run func(string) (metadataRows, error)) (string, error) {
	nodes := map[string]map[string]bool{}
	relationships := map[string]map[string]bool{}
	patterns := map[string]bool{}
	bytesRead := 0
	for _, query := range []string{nodePropertiesQuery, relationshipPropertiesQuery, visualizationQuery} {
		rows, err := run(query)
		if err != nil {
			return "", fmt.Errorf("running %s: %w", query, err)
		}
		for rows.Next(ctx) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			record := rows.Record()
			encoded, err := json.Marshal(record.Values)
			if err != nil {
				return "", fmt.Errorf("encoding schema metadata: %w", err)
			}
			bytesRead += len(encoded)
			// Raw procedure output repeats labels and type names for each property.
			if bytesRead > 8*schema.MaxBytes {
				return "", errors.New("schema discovery metadata exceeds collection limit")
			}
			switch query {
			case nodePropertiesQuery, relationshipPropertiesQuery:
				groups := nodes
				var labels []string
				if query == nodePropertiesQuery {
					value, _ := record.Get("nodeLabels")
					labels, err = metadataStrings(value)
				} else {
					groups = relationships
					value, _ := record.Get("relType")
					label, ok := value.(string)
					if !ok || label == "" {
						return "", errors.New("schema metadata has no relationship type")
					}
					label = strings.TrimPrefix(label, ":")
					if len(label) >= 2 && strings.HasPrefix(label, "`") && strings.HasSuffix(label, "`") {
						label = strings.ReplaceAll(label[1:len(label)-1], "``", "`")
					}
					labels = []string{label}
				}
				if err != nil || len(labels) == 0 {
					return "", errors.New("schema metadata has invalid node labels")
				}
				property := ""
				value, _ := record.Get("propertyName")
				if value != nil {
					name, ok := value.(string)
					if !ok || name == "" {
						return "", errors.New("schema metadata has invalid property name")
					}
					value, _ := record.Get("propertyTypes")
					types, err := metadataStrings(value)
					if err != nil || len(types) == 0 {
						return "", errors.New("schema metadata has invalid property types")
					}
					sort.Strings(types)
					property = cypherIdentifier(name) + ": " + strings.Join(types, " | ")
				}
				for _, label := range labels {
					if groups[label] == nil {
						groups[label] = map[string]bool{}
					}
					if property != "" {
						groups[label][property] = true
					}
				}
			case visualizationQuery:
				if err := addSchemaPatterns(record, patterns); err != nil {
					return "", err
				}
			}
		}
		if err := rows.Err(); err != nil {
			return "", fmt.Errorf("reading %s: %w", query, err)
		}
	}
	if len(nodes) == 0 {
		return "", errors.New("Neo4j schema discovery found no node labels")
	}
	text := "Node properties (observed in the database; properties may be optional):\n" +
		formatProperties(nodes) +
		"\nRelationship properties (observed in the database):\n" + formatProperties(relationships) +
		"\nDirected relationships (metadata-inferred; not verified against actual edges):\n" +
		strings.Join(sortedKeys(patterns), "\n") +
		"\nMetadata does not establish business meaning or prove an attack path.\n"
	if len(text) > schema.MaxBytes {
		return "", fmt.Errorf("discovered schema exceeds %d bytes", schema.MaxBytes)
	}
	return text, nil
}

func addSchemaPatterns(record *neodriver.Record, patterns map[string]bool) error {
	value, _ := record.Get("nodes")
	nodes, ok := value.([]any)
	if !ok {
		return errors.New("schema visualization has invalid nodes")
	}
	labels := make(map[int64]string, len(nodes))
	for _, value := range nodes {
		node, ok := value.(neodriver.Node)
		if !ok {
			return errors.New("schema visualization has invalid node")
		}
		name, ok := node.Props["name"].(string)
		if !ok || name == "" {
			return errors.New("schema visualization node has no label")
		}
		labels[node.Id] = name
	}
	value, _ = record.Get("relationships")
	relationships, ok := value.([]any)
	if !ok {
		return errors.New("schema visualization has invalid relationships")
	}
	for _, value := range relationships {
		rel, ok := value.(neodriver.Relationship)
		if !ok || labels[rel.StartId] == "" || labels[rel.EndId] == "" || rel.Type == "" {
			return errors.New("schema visualization has invalid relationship endpoints")
		}
		patterns[fmt.Sprintf("(:%s)-[:%s]->(:%s)",
			cypherIdentifier(labels[rel.StartId]), cypherIdentifier(rel.Type), cypherIdentifier(labels[rel.EndId]))] = true
	}
	return nil
}

func metadataStrings(value any) ([]string, error) {
	values, ok := value.([]any)
	if !ok {
		return nil, errors.New("expected schema metadata string list")
	}
	texts := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok || text == "" {
			return nil, errors.New("schema metadata contains an invalid string")
		}
		texts = append(texts, text)
	}
	return texts, nil
}

func cypherIdentifier(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}

func formatProperties(groups map[string]map[string]bool) string {
	lines := make([]string, 0, len(groups))
	for _, label := range sortedKeys(groups) {
		lines = append(lines, cypherIdentifier(label)+" {"+strings.Join(sortedKeys(groups[label]), ", ")+"}")
	}
	return strings.Join(lines, "\n") + "\n"
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
