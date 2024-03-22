package utils

import "github.com/Azure/azure-sdk-for-go/sdk/azcore/to"

func ConvertToSliceOfPointers[T any](ptrToSlice *[]T) []*T {
	var ret []*T
	if ptrToSlice == nil {
		return ret
	}

	for _, v := range *ptrToSlice {
		ret = append(ret, to.Ptr(v))
	}

	return ret
}

func ConvertToPointerOfSlice[T any](sliceToPtr []*T) *[]T {
	var ret []T
	if sliceToPtr == nil {
		return nil
	}

	for _, v := range sliceToPtr {
		ret = append(ret, *v)
	}

	return to.Ptr(ret)
}
