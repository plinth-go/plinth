package kernel

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

type apiSpec struct {
	OpenAPI    string                             `json:"openapi"`
	Info       apiInfo                            `json:"info"`
	Paths      map[string]map[string]apiOperation `json:"paths"`
	Components apiComponents                      `json:"components,omitempty"`
}

type apiInfo struct {
	Title       string `json:"title"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}

type apiComponents struct {
	Schemas map[string]any `json:"schemas,omitempty"`
}

type apiOperation struct {
	OperationID         string                 `json:"operationId,omitempty"`
	Summary             string                 `json:"summary,omitempty"`
	Description         string                 `json:"description,omitempty"`
	Tags                []string               `json:"tags,omitempty"`
	RequiredPermissions []string               `json:"x-required-permissions,omitempty"`
	Parameters          []apiParameter         `json:"parameters,omitempty"`
	RequestBody         *apiRequestBody        `json:"requestBody,omitempty"`
	Responses           map[string]apiResponse `json:"responses"`
}

type apiParameter struct {
	Name        string         `json:"name"`
	In          string         `json:"in"`
	Required    bool           `json:"required,omitempty"`
	Description string         `json:"description,omitempty"`
	Schema      map[string]any `json:"schema"`
}

type apiRequestBody struct {
	Required bool                    `json:"required"`
	Content  map[string]apiMediaType `json:"content"`
}

type apiMediaType struct {
	Schema any `json:"schema"`
}

type apiResponse struct {
	Description string                  `json:"description"`
	Content     map[string]apiMediaType `json:"content,omitempty"`
}

func buildOpenAPI(title, version string, ops []OperationMeta) []byte {
	spec := apiSpec{
		OpenAPI: "3.1.0",
		Info: apiInfo{
			Title:   title,
			Version: version,
		},
		Paths: map[string]map[string]apiOperation{},
		Components: apiComponents{Schemas: map[string]any{
			"Problem": problemSchema(),
		}},
	}

	for _, meta := range ops {
		if meta.Hidden {
			continue
		}
		pathItem, ok := spec.Paths[meta.Path]
		if !ok {
			pathItem = map[string]apiOperation{}
			spec.Paths[meta.Path] = pathItem
		}

		op := apiOperation{
			OperationID:         meta.OperationID,
			Summary:             meta.Summary,
			Description:         meta.Description,
			Tags:                meta.Tags,
			RequiredPermissions: meta.RequiredPermissions,
			Responses:           map[string]apiResponse{},
		}
		if op.Summary == "" {
			op.Summary = strings.ToUpper(meta.Method) + " " + meta.Path
		}

		for _, p := range meta.Params {
			op.Parameters = append(op.Parameters, apiParameter{
				Name: p.Name, In: p.In, Required: p.Required,
				Description: p.Description, Schema: p.Schema,
			})
		}
		if meta.Body != nil {
			op.RequestBody = &apiRequestBody{
				Required: meta.Body.Required,
				Content: map[string]apiMediaType{
					"application/json": {Schema: meta.Body.Schema},
				},
			}
		}

		status := meta.Status
		if status == 0 {
			status = http.StatusOK
		}
		if isBodylessStatus(status) {
			op.Responses[statusString(status)] = apiResponse{Description: http.StatusText(status)}
		} else {
			resp := apiResponse{Description: http.StatusText(status)}
			if len(meta.Produces) > 0 {
				resp.Content = map[string]apiMediaType{}
				for _, contentType := range meta.Produces {
					schema := any(map[string]any{"type": "string"})
					if meta.ResponseSchema != nil {
						schema = meta.ResponseSchema
					}
					resp.Content[contentType] = apiMediaType{Schema: schema}
				}
			} else if meta.ResponseSchema != nil {
				resp.Content = map[string]apiMediaType{
					"application/json": {Schema: meta.ResponseSchema},
				}
			}
			op.Responses[statusString(status)] = resp
		}
		op.Responses["422"] = apiResponse{
			Description: "Validation failed",
			Content: map[string]apiMediaType{
				"application/problem+json": {Schema: refTo("Problem")},
			},
		}
		if meta.Body != nil {
			op.Responses["415"] = apiResponse{
				Description: http.StatusText(http.StatusUnsupportedMediaType),
				Content: map[string]apiMediaType{
					"application/problem+json": {Schema: refTo("Problem")},
				},
			}
		}
		op.Responses["500"] = apiResponse{
			Description: "Internal server error",
			Content: map[string]apiMediaType{
				"application/problem+json": {Schema: refTo("Problem")},
			},
		}

		pathItem[strings.ToLower(meta.Method)] = op
	}

	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return []byte(`{"openapi":"3.1.0","info":{"title":"","version":""},"paths":{}}`)
	}
	return data
}

func statusString(code int) string {
	switch code {
	case 200:
		return "200"
	case 201:
		return "201"
	case 202:
		return "202"
	case 400:
		return "400"
	case 401:
		return "401"
	case 403:
		return "403"
	case 404:
		return "404"
	case 409:
		return "409"
	case 422:
		return "422"
	case 500:
		return "500"
	default:
		return strconv.Itoa(code)
	}
}

func refTo(name string) map[string]any {
	return map[string]any{"$ref": "#/components/schemas/" + name}
}

func problemSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"type":     map[string]any{"type": "string"},
			"title":    map[string]any{"type": "string"},
			"status":   map[string]any{"type": "integer"},
			"detail":   map[string]any{"type": "string"},
			"instance": map[string]any{"type": "string"},
			"errors": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"location": map[string]any{"type": "string"},
						"field":    map[string]any{"type": "string"},
						"message":  map[string]any{"type": "string"},
					},
				},
			},
		},
	}
}
