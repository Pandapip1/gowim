module github.com/gavin-john/gowim/driver

go 1.22

require (
	github.com/gavin-john/gowim/cat v0.0.0
	github.com/gavin-john/gowim/inf v0.0.0
	github.com/gavin-john/gowim/pe v0.0.0
	github.com/gavin-john/gowim/regf v0.0.0
	github.com/gavin-john/gowim/wim v0.0.0
)

replace (
	github.com/gavin-john/gowim/cat => ../cat
	github.com/gavin-john/gowim/inf => ../inf
	github.com/gavin-john/gowim/pe => ../pe
	github.com/gavin-john/gowim/regf => ../regf
	github.com/gavin-john/gowim/wim => ../wim
)
