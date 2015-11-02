package riak

// RpbGetBucketReq

func (m *RpbGetBucketReq) GetKey() []byte {
	return nil
}

func (m *RpbGetBucketReq) SetType(bt []byte) {
	m.Type = bt
}

func (m *RpbGetBucketReq) BucketIsRequired() bool {
	return true
}

func (m *RpbGetBucketReq) KeyIsRequired() bool {
	return false
}

// RpbSetBucketReq

func (m *RpbSetBucketReq) GetKey() []byte {
	return nil
}

func (m *RpbSetBucketReq) SetType(bt []byte) {
	m.Type = bt
}

func (m *RpbSetBucketReq) BucketIsRequired() bool {
	return true
}

func (m *RpbSetBucketReq) KeyIsRequired() bool {
	return false
}

// RpbResetBucketReq

func (m *RpbResetBucketReq) GetKey() []byte {
	return nil
}

func (m *RpbResetBucketReq) SetType(bt []byte) {
	m.Type = bt
}

func (m *RpbResetBucketReq) BucketIsRequired() bool {
	return true
}

func (m *RpbResetBucketReq) KeyIsRequired() bool {
	return false
}
