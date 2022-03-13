package model

type ServiceItemResponse struct {
	TotalUp     uint64
	TotalDown   uint64
	CurrentUp   uint64
	CurrentDown uint64
	Delay       *[30]float32
	Up          *[30]int
	Down        *[30]int
}
