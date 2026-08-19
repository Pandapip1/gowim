module github.com/Pandapip1/gowim/component

go 1.22

require (
	github.com/Pandapip1/gowim/mum v0.0.0
	github.com/Pandapip1/gowim/pa30 v0.0.0
	github.com/Pandapip1/gowim/regf v0.0.0
	github.com/Pandapip1/gowim/wim v0.0.0
)

require (
	github.com/Pandapip1/gowim/lzms v0.0.0 // indirect
	github.com/Pandapip1/gowim/lzx v0.0.0 // indirect
	github.com/Pandapip1/gowim/xpress v0.0.0 // indirect
)

replace (
	github.com/Pandapip1/gowim/lzms => ../lzms
	github.com/Pandapip1/gowim/lzx => ../lzx
	github.com/Pandapip1/gowim/mum => ../mum
	github.com/Pandapip1/gowim/pa30 => ../pa30
	github.com/Pandapip1/gowim/regf => ../regf
	github.com/Pandapip1/gowim/wim => ../wim
	github.com/Pandapip1/gowim/xpress => ../xpress
)
