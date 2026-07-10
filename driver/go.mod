module github.com/Pandapip1/gowim/driver

go 1.22

require (
	github.com/Pandapip1/gowim/cat v0.0.0
	github.com/Pandapip1/gowim/inf v0.0.0
	github.com/Pandapip1/gowim/pe v0.0.0
	github.com/Pandapip1/gowim/regf v0.0.0
	github.com/Pandapip1/gowim/service v0.0.0
	github.com/Pandapip1/gowim/wim v0.0.0
)

replace (
	github.com/Pandapip1/gowim/cat => ../cat
	github.com/Pandapip1/gowim/inf => ../inf
	github.com/Pandapip1/gowim/pe => ../pe
	github.com/Pandapip1/gowim/regf => ../regf
	github.com/Pandapip1/gowim/service => ../service
	github.com/Pandapip1/gowim/wim => ../wim
)
