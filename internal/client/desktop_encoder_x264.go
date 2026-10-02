//go:build amd64 || arm64

package client

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Software H.264 backend backed by libx264 loaded at runtime through purego.
// The default build stays CGO_ENABLED=0; when no usable libx264 shared
// library is found the desktop session falls back to MJPEG.

const (
	x264CspI420    = 0x0002
	x264RcCrf      = 1
	x264LogNone    = -1
	x264NalTypeSPS = 7
	x264NalTypePPS = 8
)

var errX264Unavailable = errors.New("libx264 not available")

type x264Vui struct {
	ISarHeight int32
	ISarWidth  int32
	IOverScan  int32
	IVidFormat int32
	BFullRange int32
	IColorPrim int32
	ITransfer  int32
	IColMatrix int32
	IChromaLoc int32
}

type x264Analyse struct {
	Intra            uint32
	Inter            uint32
	BTransform8x8    int32
	IWeightedPred    int32
	BWeightedBipred  int32
	IDirectMvPred    int32
	IChromaQpOffset  int32
	IMeMethod        int32
	IMeRange         int32
	IMvRange         int32
	IMvRangeThread   int32
	ISubpelRefine    int32
	BChromaMe        int32
	BMixedReferences int32
	ITrellis         int32
	BFastPskip       int32
	BDctDecimate     int32
	INoiseReduction  int32
	FPsyRd           float32
	FPsyTrellis      float32
	BPsy             int32
	BMbInfo          int32
	BMbInfoUpdate    int32
	ILumaDeadzone    [2]int32
	BPsnr            int32
	BSsim            int32
}

type x264Rc struct {
	IRcMethod       int32
	IQpConstant     int32
	IQpMin          int32
	IQpMax          int32
	IQpStep         int32
	IBitrate        int32
	FRfConstant     float32
	FRfConstantMax  float32
	RateTolerance   float32
	IVbvMaxBitrate  int32
	IVbvBufferSize  int32
	FVbvBufferInit  float32
	FIpFactor       float32
	FPbFactor       float32
	BFiller         int32
	IAqMode         int32
	FAqStrength     float32
	BMbTree         int32
	ILookahead      int32
	BStatWrite      int32
	PszStatOut      *byte
	BStatRead       int32
	PszStatIn       *byte
	FQcompress      float32
	FQblur          float32
	FComplexityBlur float32
	Zones           unsafe.Pointer
	IZones          int32
	PszZones        *byte
}

type x264Param struct {
	CPU               uint32
	IThreads          int32
	ILookaheadThreads int32
	BSlicedThreads    int32
	BDeterministic    int32
	BCpuIndependent   int32
	ISyncLookahead    int32

	IWidth      int32
	IHeight     int32
	ICsp        int32
	IBitdepth   int32
	ILevelIdc   int32
	IFrameTotal int32

	INalHrd int32

	Vui x264Vui

	IFrameReference    int32
	IDpbSize           int32
	IKeyintMax         int32
	IKeyintMin         int32
	IScenecutThreshold int32
	BIntraRefresh      int32

	IBframe         int32
	IBframeAdaptive int32
	IBframeBias     int32
	IBframePyramid  int32
	BOpenGop        int32
	BBlurayCompat   int32
	IAvcintraClass  int32
	IAvcintraFlavor int32

	BDeblockingFilter        int32
	IDeblockingFilterAlphac0 int32
	IDeblockingFilterBeta    int32

	BCabac        int32
	ICabacInitIdc int32

	BInterlaced       int32
	BConstrainedIntra int32

	ICqmPreset int32
	PszCqmFile *byte
	Cqm4iy     [16]uint8
	Cqm4py     [16]uint8
	Cqm4ic     [16]uint8
	Cqm4pc     [16]uint8
	Cqm8iy     [64]uint8
	Cqm8py     [64]uint8
	Cqm8ic     [64]uint8
	Cqm8pc     [64]uint8

	PfLog       uintptr
	PLogPrivate unsafe.Pointer
	ILogLevel   int32
	BFullRecon  int32
	PszDumpYuv  *byte

	Analyse x264Analyse

	Rc x264Rc

	CropRect struct {
		ILeft   int32
		ITop    int32
		IRight  int32
		IBottom int32
	}

	IFramePacking int32

	MasteringDisplay struct {
		BMasteringDisplay int32
		IGreenX           int32
		IGreenY           int32
		IBlueX            int32
		IBlueY            int32
		IRedX             int32
		IRedY             int32
		IWhiteX           int32
		IWhiteY           int32
		IDisplayMax       int64
		IDisplayMin       int64
	}

	ContentLightLevel struct {
		BCll     int32
		IMaxCll  int32
		IMaxFall int32
	}

	IAlternativeTransfer int32

	BAud           int32
	BRepeatHeaders int32
	BAnnexb        int32
	ISpsId         int32
	BVfrInput      int32
	BPulldown      int32
	IFpsNum        uint32
	IFpsDen        uint32
	ITimebaseNum   uint32
	ITimebaseDen   uint32

	BTff            int32
	BPicStruct      int32
	BFakeInterlaced int32
	BStitchable     int32

	BOpencl        int32
	IOpenclDevice  int32
	OpenclDeviceID unsafe.Pointer
	PszClbinFile   *byte

	ISliceMaxSize  int32
	ISliceMaxMbs   int32
	ISliceMinMbs   int32
	ISliceCount    int32
	ISliceCountMax int32

	ParamFree   uintptr
	NaluProcess uintptr
	Opaque      unsafe.Pointer
}

type x264Image struct {
	ICsp    int32
	IPlane  int32
	IStride [4]int32
	Plane   [4]*byte
}

type x264ImageProps struct {
	QuantOffsets     *float32
	QuantOffsetsFree uintptr
	MbInfo           *byte
	MbInfoFree       uintptr
	FSsim            float64
	FPsnrAvg         float64
	FPsnr            [3]float64
	FCrfAvg          float64
}

type x264Hrd struct {
	CpbInitialArrivalTime float64
	CpbFinalArrivalTime   float64
	CpbRemovalTime        float64
	DpbOutputTime         float64
}

type x264Sei struct {
	NumPayloads int32
	_           [4]byte
	Payloads    unsafe.Pointer
	SeiFree     uintptr
}

type x264Picture struct {
	IType      int32
	IQpplus1   int32
	IPicStruct int32
	BKeyframe  int32
	IPts       int64
	IDts       int64
	Param      unsafe.Pointer
	Img        x264Image
	Prop       x264ImageProps
	HrdTiming  x264Hrd
	ExtraSei   x264Sei
	Opaque     unsafe.Pointer
}

type x264Nal struct {
	IRefIdc        int32
	IType          int32
	BLongStartcode int32
	IFirstMb       int32
	ILastMb        int32
	IPayload       int32
	PPayload       *byte
	IPadding       int32
	_              [4]byte
}

type x264API struct {
	handle             uintptr
	encoderOpen        func(param unsafe.Pointer) unsafe.Pointer // versioned symbol
	paramDefaultPreset func(param unsafe.Pointer, preset, tune *byte) int32
	paramApplyProfile  func(param unsafe.Pointer, profile *byte) int32
	pictureInit        func(pic unsafe.Pointer)
	encoderHeaders     func(h unsafe.Pointer, ppNal **x264Nal, piNal *int32) int32
	encoderEncode      func(h unsafe.Pointer, ppNal **x264Nal, piNal *int32, picIn, picOut unsafe.Pointer) int32
	encoderClose       func(h unsafe.Pointer)
}

var (
	x264Once   sync.Once
	x264Loaded *x264API
)

func loadX264API() (*x264API, bool) {
	x264Once.Do(func() {
		x264Loaded = probeX264API()
	})
	return x264Loaded, x264Loaded != nil
}

func probeX264API() *x264API {
	for _, name := range x264LibraryCandidates() {
		handle, ok := openX264Library(name)
		if !ok {
			continue
		}
		api := &x264API{}
		// x264_encoder_open is a versioned symbol (x264_encoder_open_<build>).
		// Probe the known build range newest first.
		for version := 170; version >= 150; version-- {
			sym, err := lookupX264Symbol(handle, fmt.Sprintf("x264_encoder_open_%d", version))
			if err == nil {
				purego.RegisterFunc(&api.encoderOpen, sym)
				break
			}
		}
		if api.encoderOpen == nil {
			continue
		}
		bindings := []struct {
			name string
			dst  any
		}{
			{"x264_param_default_preset", &api.paramDefaultPreset},
			{"x264_param_apply_profile", &api.paramApplyProfile},
			{"x264_picture_init", &api.pictureInit},
			{"x264_encoder_headers", &api.encoderHeaders},
			{"x264_encoder_encode", &api.encoderEncode},
			{"x264_encoder_close", &api.encoderClose},
		}
		bound := true
		for _, binding := range bindings {
			// RegisterLibFunc panics on missing symbols; probe first so an
			// incompatible library can be skipped gracefully.
			if _, err := lookupX264Symbol(handle, binding.name); err != nil {
				bound = false
				break
			}
		}
		if !bound {
			continue
		}
		for _, binding := range bindings {
			purego.RegisterLibFunc(binding.dst, handle, binding.name)
		}
		api.handle = handle
		return api
	}
	return nil
}

func cString(s string) *byte {
	b := append([]byte(s), 0)
	return &b[0]
}

type x264Encoder struct {
	api          *x264API
	handle       unsafe.Pointer
	width        int
	height       int
	chromaWidth  int
	chromaHeight int
	chromaStride int
	planeY       []byte
	planeU       []byte
	planeV       []byte
	picIn        x264Picture
	picOut       x264Picture
	pts          int64
	codecInfo    desktopCodecInfo
}

func newX264Encoder(width, height, quality, fps int) (desktopEncoder, error) {
	api, ok := loadX264API()
	if !ok {
		return nil, errX264Unavailable
	}
	if width < 2 || height < 2 || width&1 != 0 || height&1 != 0 {
		return nil, fmt.Errorf("H.264 requires positive even desktop frame dimensions, got %dx%d", width, height)
	}
	if fps <= 0 {
		fps = 2
	}
	if fps > 12 {
		fps = 12
	}

	var param x264Param
	preset := cString("veryfast")
	tune := cString("zerolatency")
	if api.paramDefaultPreset(unsafe.Pointer(&param), preset, tune) != 0 {
		return nil, errors.New("x264_param_default_preset rejected preset")
	}
	param.IWidth = int32(width)
	param.IHeight = int32(height)
	param.ICsp = x264CspI420
	param.IBitdepth = 8
	param.IFpsNum = uint32(fps)
	param.IFpsDen = 1
	param.ITimebaseNum = 1
	param.ITimebaseDen = uint32(fps)
	param.IKeyintMax = int32(max(20, fps*2))
	param.BRepeatHeaders = 0
	param.BAnnexb = 0
	param.ILogLevel = x264LogNone
	param.Rc.IRcMethod = x264RcCrf
	crf := 38 - float32(quality)*0.22
	crf = min(max(crf, 18), 32)
	param.Rc.FRfConstant = crf
	profile := cString("baseline")
	if api.paramApplyProfile(unsafe.Pointer(&param), profile) != 0 {
		return nil, errors.New("x264_param_apply_profile failed")
	}
	handle := api.encoderOpen(unsafe.Pointer(&param))
	if handle == nil {
		return nil, errors.New("x264_encoder_open failed")
	}

	enc := &x264Encoder{
		api:          api,
		handle:       handle,
		width:        width,
		height:       height,
		chromaWidth:  (width + 1) / 2,
		chromaHeight: (height + 1) / 2,
	}
	enc.chromaStride = enc.chromaWidth
	enc.planeY = make([]byte, width*height)
	enc.planeU = make([]byte, enc.chromaStride*enc.chromaHeight)
	enc.planeV = make([]byte, enc.chromaStride*enc.chromaHeight)
	api.pictureInit(unsafe.Pointer(&enc.picIn))
	api.pictureInit(unsafe.Pointer(&enc.picOut))

	var nalPtr *x264Nal
	var nalCount int32
	headersRC := api.encoderHeaders(handle, &nalPtr, &nalCount)
	if headersRC < 0 || nalCount < 2 {
		enc.Close()
		return nil, fmt.Errorf("x264_encoder_headers failed: rc=%d nals=%d", headersRC, nalCount)
	}
	sps, pps, err := extractSPSPPS(nalPtr, nalCount)
	if err != nil {
		enc.Close()
		return nil, err
	}
	enc.codecInfo = desktopCodecInfo{
		Codec:       avcCodecString(sps),
		Description: buildAVCC(sps, pps),
	}
	return enc, nil
}

func extractSPSPPS(nalPtr *x264Nal, count int32) (sps, pps []byte, err error) {
	if nalPtr == nil || count <= 0 {
		return nil, nil, errors.New("x264 headers contain no NAL units")
	}
	for i := range int(count) {
		nal := (*x264Nal)(unsafe.Add(unsafe.Pointer(nalPtr), uintptr(i)*unsafe.Sizeof(x264Nal{})))
		if nal.PPayload == nil || nal.IPayload <= 4 {
			continue
		}
		payload := unsafe.Slice(nal.PPayload, nal.IPayload)
		payload, err := stripNALPrefix(payload)
		if err != nil {
			return nil, nil, err
		}
		switch nal.IType {
		case x264NalTypeSPS:
			sps = bytes.Clone(payload)
		case x264NalTypePPS:
			pps = bytes.Clone(payload)
		}
	}
	if len(sps) < 4 || len(pps) == 0 {
		return nil, nil, fmt.Errorf("x264 headers missing SPS/PPS")
	}
	return sps, pps, nil
}

// stripNALPrefix removes the 4-byte length prefix that x264 (b_annexb=0)
// places before every encoded NAL buffer.
func stripNALPrefix(payload []byte) ([]byte, error) {
	if len(payload) >= 4 {
		if prefix := binary.BigEndian.Uint32(payload[:4]); int(prefix) == len(payload)-4 {
			return payload[4:], nil
		}
	}
	return nil, fmt.Errorf("x264 NAL missing length prefix (%d bytes)", len(payload))
}

// avcCodecString derives a WebCodecs codec string from the SPS sequence
// parameter set bytes, e.g. "avc1.42C01F".
func avcCodecString(sps []byte) string {
	const hexDigits = "0123456789ABCDEF"
	out := make([]byte, 0, 14)
	out = append(out, "avc1."...)
	for _, b := range sps[1:4] {
		out = append(out, hexDigits[b>>4], hexDigits[b&0x0F])
	}
	return string(out)
}

// buildAVCC assembles an AVCDecoderConfigurationRecord (extradata) for the
// browser WebCodecs decoder from the raw SPS/PPS NAL payloads.
func buildAVCC(sps, pps []byte) []byte {
	out := make([]byte, 0, 11+len(sps)+5+len(pps))
	out = append(out, 0x01, sps[1], sps[2], sps[3], 0xFF, 0xE1)
	var lenPrefix [2]byte
	binary.BigEndian.PutUint16(lenPrefix[:], uint16(len(sps)))
	out = append(out, lenPrefix[:]...)
	out = append(out, sps...)
	out = append(out, 0x01)
	binary.BigEndian.PutUint16(lenPrefix[:], uint16(len(pps)))
	out = append(out, lenPrefix[:]...)
	out = append(out, pps...)
	return out
}

func (e *x264Encoder) Format() string { return desktopFormatH264 }

func (e *x264Encoder) CodecInfo() desktopCodecInfo { return e.codecInfo }

func (e *x264Encoder) Close() {
	if e.handle != nil {
		e.api.encoderClose(e.handle)
		e.handle = nil
	}
}

// Encode converts one RGBA frame to I420, feeds it to x264 and appends
// [keyframe flag byte][4-byte BE length][NAL]... access unit data to out.
func (e *x264Encoder) Encode(frame *image.RGBA, out *bytes.Buffer) error {
	if e.handle == nil {
		return errors.New("x264 encoder is closed")
	}
	if frame == nil {
		return errors.New("x264 frame is nil")
	}
	if frame.Bounds().Dx() != e.width || frame.Bounds().Dy() != e.height {
		return fmt.Errorf("x264 frame size %dx%d does not match encoder %dx%d", frame.Bounds().Dx(), frame.Bounds().Dy(), e.width, e.height)
	}
	if frame.Stride < e.width*4 || len(frame.Pix) < frame.Stride*e.height {
		return fmt.Errorf("x264 frame pixels do not cover %dx%d RGBA image", e.width, e.height)
	}
	e.fillI420(frame)
	e.picIn.IPts = e.pts
	e.pts++
	var nalPtr *x264Nal
	var nalCount int32
	rc := e.api.encoderEncode(e.handle, &nalPtr, &nalCount, unsafe.Pointer(&e.picIn), unsafe.Pointer(&e.picOut))
	if rc < 0 {
		return fmt.Errorf("x264_encoder_encode failed: rc=%d", rc)
	}
	if nalCount == 0 || nalPtr == nil {
		return errors.New("x264_encoder_encode produced no NAL units")
	}
	if e.picOut.BKeyframe != 0 {
		out.WriteByte(1)
	} else {
		out.WriteByte(0)
	}
	// With b_annexb=0 each NAL buffer already carries its 4-byte big-endian
	// length prefix; the access unit is the concatenation of those buffers.
	for i := range int(nalCount) {
		nal := (*x264Nal)(unsafe.Add(unsafe.Pointer(nalPtr), uintptr(i)*unsafe.Sizeof(x264Nal{})))
		if nal.IPayload <= 4 || nal.PPayload == nil {
			continue
		}
		payload := unsafe.Slice(nal.PPayload, nal.IPayload)
		if int(binary.BigEndian.Uint32(payload[:4])) != len(payload)-4 {
			return fmt.Errorf("x264 NAL has invalid length prefix: prefix=%d payload=%d", binary.BigEndian.Uint32(payload[:4]), len(payload))
		}
		out.Write(payload)
	}
	if out.Len() == 1 {
		return errors.New("x264_encoder_encode produced empty access unit")
	}
	return nil
}

// fillI420 converts the RGBA frame to planar 4:2:0 (BT.601 limited range)
// and wires the planes into picIn.Img.
func (e *x264Encoder) fillI420(frame *image.RGBA) {
	w, h := e.width, e.height
	pix := frame.Pix
	stride := frame.Stride
	y := e.planeY
	u := e.planeU
	v := e.planeV
	cw := e.chromaStride
	// Each iteration covers one 2x2 luma block row pair.
	blocks := (h + 1) / 2
	parallelDesktopRows(w, blocks, func(b0, b1 int) {
		for b := b0; b < b1; b++ {
			row0 := b * 2
			src0 := pix[row0*stride:]
			src1 := src0
			if row0+1 < h && row0+1 < frame.Rect.Dy() {
				src1 = pix[(row0+1)*stride:]
			}
			yOff := row0 * w
			cOff := b * cw
			for x := range w {
				px := x * 4
				r0, g0, b0 := int(src0[px]), int(src0[px+1]), int(src0[px+2])
				y[yOff+x] = desktopClampYUV(((66*r0 + 129*g0 + 25*b0 + 128) >> 8) + 16)
				if x&1 == 1 || x+1 >= w {
					continue
				}
				px1 := px + 4
				r1, g1, b1 := int(src0[px1]), int(src0[px1+1]), int(src0[px1+2])
				r2, g2, b2 := int(src1[px]), int(src1[px+1]), int(src1[px+2])
				r3, g3, b3 := int(src1[px1]), int(src1[px1+1]), int(src1[px1+2])
				rAvg := (r0 + r1 + r2 + r3) >> 2
				gAvg := (g0 + g1 + g2 + g3) >> 2
				bAvg := (b0 + b1 + b2 + b3) >> 2
				u[cOff+x>>1] = desktopClampUV(((-38*rAvg - 74*gAvg + 112*bAvg + 128) >> 8) + 128)
				v[cOff+x>>1] = desktopClampUV(((112*rAvg - 94*gAvg - 18*bAvg + 128) >> 8) + 128)
			}
		}
	})
	e.picIn.Img.ICsp = x264CspI420
	e.picIn.Img.IPlane = 3
	e.picIn.Img.IStride[0] = int32(w)
	e.picIn.Img.IStride[1] = int32(cw)
	e.picIn.Img.IStride[2] = int32(cw)
	e.picIn.Img.IStride[3] = 0
	e.picIn.Img.Plane[0] = &e.planeY[0]
	e.picIn.Img.Plane[1] = &e.planeU[0]
	e.picIn.Img.Plane[2] = &e.planeV[0]
	e.picIn.Img.Plane[3] = nil
}

func desktopClampYUV(value int) byte {
	if value < 0 {
		return 0
	}
	if value > 255 {
		return 255
	}
	return byte(value)
}

func desktopClampUV(value int) byte {
	if value < 0 {
		return 0
	}
	if value > 255 {
		return 255
	}
	return byte(value)
}
