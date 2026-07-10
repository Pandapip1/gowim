module github.com/Pandapip1/gowim/wim

go 1.22

require (
	github.com/Pandapip1/gowim/lzms v0.0.0
	github.com/Pandapip1/gowim/lzx v0.0.0
	github.com/Pandapip1/gowim/xpress v0.0.0
)

replace (
	github.com/Pandapip1/gowim/lzms => ../lzms
	github.com/Pandapip1/gowim/lzx => ../lzx
	github.com/Pandapip1/gowim/xpress => ../xpress
)
