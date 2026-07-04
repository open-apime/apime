package dashboard

import (
	"html/template"
	"time"

	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/storage/model"
)

var (
	dashboardLocation = time.Local
	dashboardTimezone string
)

func configureTimezone(tz string, logger *zap.Logger) string {
	if tz == "" {
		dashboardTimezone = ""
		dashboardLocation = time.Local
		return ""
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		if logger != nil {
			logger.Warn("timezone inválido para dashboard, usando local", zap.String("timezone", tz), zap.Error(err))
		}
		dashboardTimezone = ""
		dashboardLocation = time.Local
		return ""
	}
	dashboardLocation = loc
	dashboardTimezone = tz
	if logger != nil {
		logger.Info("timezone do dashboard configurado", zap.String("timezone", tz))
	}
	return tz
}

func currentTimezone() string {
	return dashboardTimezone
}

func currentLocation() *time.Location {
	if dashboardLocation != nil {
		return dashboardLocation
	}
	return time.Local
}

type Flash struct {
	Type    string
	Message string
}

func HTMLTemplate() *template.Template {
	return template.Must(template.New("T").Funcs(templateFuncMap()).ParseFS(templateFS, "templates/*.tmpl"))
}

func templateFuncMap() template.FuncMap {
	return template.FuncMap{
		"formatTime": func(t time.Time) string {
			if t.IsZero() {
				return "-"
			}
			return t.In(currentLocation()).Format("02/01/2006 15:04")
		},
		"formatOptionalTime": func(t *time.Time) string {
			if t == nil {
				return "—"
			}
			return t.In(currentLocation()).Format("02/01/2006 15:04")
		},
		"statusBadge": func(status model.InstanceStatus) string {
			switch status {
			case model.InstanceStatusActive:
				return "status-active"
			case model.InstanceStatusPending:
				return "status-pending"
			case model.InstanceStatusDisconnected:
				return "status-disconnected"
			default:
				return "status-error"
			}
		},
		"translateStatus": func(status model.InstanceStatus) string {
			switch status {
			case model.InstanceStatusActive:
				return "Ativo"
			case model.InstanceStatusPending:
				return "Aguardando"
			case model.InstanceStatusDisconnected:
				return "Desconectado"
			case model.InstanceStatusError:
				return "Erro"
			default:
				return string(status)
			}
		},
		"div": func(a, b any) float64 {
			toFloat := func(v any) float64 {
				switch i := v.(type) {
				case int:
					return float64(i)
				case int8:
					return float64(i)
				case int16:
					return float64(i)
				case int32:
					return float64(i)
				case int64:
					return float64(i)
				case uint:
					return float64(i)
				case uint8:
					return float64(i)
				case uint16:
					return float64(i)
				case uint32:
					return float64(i)
				case uint64:
					return float64(i)
				case float32:
					return float64(i)
				case float64:
					return i
				default:
					return 0
				}
			}
			valA := toFloat(a)
			valB := toFloat(b)
			if valB == 0 {
				return 0
			}
			return valA / valB
		},
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
		"seq": func(start, end int) []int {
			var res []int
			for i := start; i <= end; i++ {
				res = append(res, i)
			}
			return res
		},
	}
}
