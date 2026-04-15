package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ugurcan-aytar/anvil/internal/engine"
)

// ConfigFilename is the project-local config file that lives
// alongside .anvil/index.db. JSON keeps the surface area small and
// avoids pulling in a YAML dependency just for a handful of scalars.
const ConfigFilename = "config.json"

// Config is every value `anvil config set` / `get` / `list` can
// touch. Zero values mean "unset" — readers should fall through to
// env vars or hardcoded defaults. Adding a field here + registering
// it in the configFields map below is all that's needed for a new
// key to be recognised across the command surface.
type Config struct {
	Model    string  `json:"model,omitempty"`
	TopK     int     `json:"topk,omitempty"`
	MinScore float64 `json:"min-score,omitempty"`
	Workers  int     `json:"workers,omitempty"`
	AutoSave bool    `json:"auto-save,omitempty"`
	Debounce int     `json:"debounce,omitempty"`
}

// configFields maps user-facing keys to a {getter, setter, help}
// triple. Tests walk this map to guarantee every declared field has
// parser + formatter coverage; runtime lookup is O(N) which is fine
// for a handful of keys.
type configField struct {
	help string
	get  func(*Config) string
	set  func(*Config, string) error
}

var configFields = map[string]configField{
	"model": {
		help: "LLM model name (ANVIL_MODEL env var still overrides)",
		get:  func(c *Config) string { return c.Model },
		set:  func(c *Config, v string) error { c.Model = v; return nil },
	},
	"topk": {
		help: "default search result count",
		get:  func(c *Config) string { return strconv.Itoa(c.TopK) },
		set: func(c *Config, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("topk must be an integer (got %q)", v)
			}
			if n < 0 {
				return fmt.Errorf("topk must be non-negative")
			}
			c.TopK = n
			return nil
		},
	},
	"min-score": {
		help: "minimum relevance score floor",
		get:  func(c *Config) string { return strconv.FormatFloat(c.MinScore, 'f', -1, 64) },
		set: func(c *Config, v string) error {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return fmt.Errorf("min-score must be a float (got %q)", v)
			}
			c.MinScore = f
			return nil
		},
	},
	"workers": {
		help: "default ingest worker count",
		get:  func(c *Config) string { return strconv.Itoa(c.Workers) },
		set: func(c *Config, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("workers must be an integer (got %q)", v)
			}
			if n < 1 {
				return fmt.Errorf("workers must be >= 1")
			}
			c.Workers = n
			return nil
		},
	},
	"auto-save": {
		help: "skip ask's 'save this answer?' prompt and always save",
		get:  func(c *Config) string { return strconv.FormatBool(c.AutoSave) },
		set: func(c *Config, v string) error {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("auto-save must be true/false (got %q)", v)
			}
			c.AutoSave = b
			return nil
		},
	},
	"debounce": {
		help: "file-watcher debounce in milliseconds",
		get:  func(c *Config) string { return strconv.Itoa(c.Debounce) },
		set: func(c *Config, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("debounce must be an integer (got %q)", v)
			}
			if n < 0 {
				return fmt.Errorf("debounce must be non-negative")
			}
			c.Debounce = n
			return nil
		},
	},
}

// LoadConfig reads .anvil/config.json. A missing file returns an
// empty Config, not an error — every value has a default, so a
// freshly-initialised project just inherits them all.
func LoadConfig(projectRoot string) (*Config, error) {
	path := filepath.Join(projectRoot, engine.DBSubdir, ConfigFilename)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &c, nil
}

// SaveConfig persists the config file. Creates .anvil/ if needed —
// `anvil config set` on a brand-new project shouldn't error just
// because the directory doesn't exist yet.
func SaveConfig(projectRoot string, c *Config) error {
	dir := filepath.Join(projectRoot, engine.DBSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, ConfigFilename)
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage project-local settings (.anvil/config.json)",
	Long: `anvil config reads and writes per-project defaults stored at
.anvil/config.json. These defaults are the lowest-priority layer:

    CLI flag > env var > config.json > hardcoded default

so ` + "`anvil config set model claude-sonnet-4-6`" + ` becomes the fallback
when a command doesn't get an --model flag or ANVIL_MODEL env var.`,
}

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a config value",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfigSet(args[0], args[1])
	},
}

var configGetCmd = &cobra.Command{
	Use:   "get <key>",
	Short: "Print a config value",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfigGet(args[0])
	},
}

var configListCmd = &cobra.Command{
	Use:   "list",
	Short: "Print every recognised config key + current value",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfigList()
	},
}

func init() {
	configCmd.AddCommand(configSetCmd)
	configCmd.AddCommand(configGetCmd)
	configCmd.AddCommand(configListCmd)
}

// resolveProject returns the absolute project root using the same
// --project flag the rest of the CLI honours. Non-existing
// directories are a hard error — `anvil config set` on a bogus
// path is almost certainly a typo.
func resolveProject() (string, error) {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("project dir %s: %w", abs, err)
	}
	return abs, nil
}

func runConfigSet(key, value string) error {
	key = strings.ToLower(strings.TrimSpace(key))
	field, ok := configFields[key]
	if !ok {
		return fmt.Errorf("unknown config key %q (run `anvil config list` for the full set)", key)
	}
	root, err := resolveProject()
	if err != nil {
		return err
	}
	c, err := LoadConfig(root)
	if err != nil {
		return err
	}
	if err := field.set(c, value); err != nil {
		return err
	}
	if err := SaveConfig(root, c); err != nil {
		return err
	}
	fmt.Printf("%s = %s\n", key, field.get(c))
	return nil
}

func runConfigGet(key string) error {
	key = strings.ToLower(strings.TrimSpace(key))
	field, ok := configFields[key]
	if !ok {
		return fmt.Errorf("unknown config key %q", key)
	}
	root, err := resolveProject()
	if err != nil {
		return err
	}
	c, err := LoadConfig(root)
	if err != nil {
		return err
	}
	fmt.Println(field.get(c))
	return nil
}

func runConfigList() error {
	root, err := resolveProject()
	if err != nil {
		return err
	}
	c, err := LoadConfig(root)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(configFields))
	for k := range configFields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Println("Project config (.anvil/config.json):")
	for _, k := range keys {
		f := configFields[k]
		val := f.get(c)
		if val == "" || val == "0" || val == "false" {
			// Friendlier rendering when the value is the zero
			// value — signals the fallback is active.
			val += "  (default)"
		}
		fmt.Printf("  %-12s %s\n", k+":", val)
	}
	fmt.Println()
	fmt.Println("Priority: CLI flag > env var > config.json > hardcoded default.")
	return nil
}
