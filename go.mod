module github.com/router-for-me/cpa-intent-router

go 1.26.0

replace github.com/router-for-me/CLIProxyAPI/v8 => ../CLIProxyAPI

require (
	github.com/router-for-me/CLIProxyAPI/v8 v8.0.0-00010101000000-000000000000
	github.com/tidwall/gjson v1.20.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
)
