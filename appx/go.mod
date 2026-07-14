module github.com/Pandapip1/gowim/appx

go 1.22

require (
	github.com/Pandapip1/gowim/regf v0.0.0
	github.com/Pandapip1/gowim/registry v0.0.0
	github.com/Pandapip1/gowim/wim v0.0.0
)

replace (
	github.com/Pandapip1/gowim/regf => ../regf
	github.com/Pandapip1/gowim/registry => ../registry
	github.com/Pandapip1/gowim/wim => ../wim
)
