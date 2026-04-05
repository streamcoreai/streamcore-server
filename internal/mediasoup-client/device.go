package mediasoupclient

import (
	"errors"
	"fmt"

	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
)

// NativeRtpCapabilitiesFunc mirrors mediasoup-client handlerFactory.getNativeRtpCapabilities.
type NativeRtpCapabilitiesFunc func(direction RTPDirection) (RtpCapabilities, error)

// NativeSctpCapabilitiesFunc mirrors mediasoup-client handlerFactory.getNativeSctpCapabilities.
type NativeSctpCapabilitiesFunc func() (SctpCapabilities, error)

// DeviceOptions configures Device.
type DeviceOptions struct {
	API                    *webrtc.API
	NativeRtpCapabilities  NativeRtpCapabilitiesFunc
	NativeSctpCapabilities NativeSctpCapabilitiesFunc
}

// Device mirrors mediasoup-client's Device and keeps the same load/create flow.
type Device struct {
	api                    *webrtc.API
	nativeRtpCapabilities  NativeRtpCapabilitiesFunc
	nativeSctpCapabilities NativeSctpCapabilitiesFunc

	loaded bool

	// getSendExtendedRtpCapabilities maps directly to Device._getSendExtendedRtpCapabilities in TS.
	getSendExtendedRtpCapabilities func(nativeSendRtpCapabilities RtpCapabilities) ExtendedRtpCapabilities

	recvRtpCapabilities RtpCapabilities
	sendRtpCapabilities RtpCapabilities
	sctpCapabilities    SctpCapabilities
	canProduceByKind    map[MediaKind]bool
}

// NewDevice creates a mediasoup-style Device backed by Pion.
func NewDevice(options DeviceOptions) (*Device, error) {
	api := options.API
	if api == nil {
		builtAPI, err := newDefaultPionAPI()
		if err != nil {
			return nil, err
		}
		api = builtAPI
	}

	nativeRtpCapabilities := options.NativeRtpCapabilities
	if nativeRtpCapabilities == nil {
		nativeRtpCapabilities = DefaultNativeRtpCapabilities
	}

	nativeSctpCapabilities := options.NativeSctpCapabilities
	if nativeSctpCapabilities == nil {
		nativeSctpCapabilities = DefaultNativeSctpCapabilities
	}

	return &Device{
		api:                    api,
		nativeRtpCapabilities:  nativeRtpCapabilities,
		nativeSctpCapabilities: nativeSctpCapabilities,
		canProduceByKind: map[MediaKind]bool{
			MediaKindAudio: false,
			MediaKindVideo: false,
		},
	}, nil
}

// Loaded reports whether Load() completed successfully.
func (d *Device) Loaded() bool {
	return d.loaded
}

// API returns the Pion API used by transports created from this Device.
func (d *Device) API() *webrtc.API {
	return d.api
}

// Load mirrors mediasoup-client Device.load().
func (d *Device) Load(routerRtpCapabilities RtpCapabilities, preferLocalCodecsOrder bool) error {
	if d.loaded {
		return errors.New("already loaded")
	}

	clonedRouterRtpCapabilities := cloneRtpCapabilities(routerRtpCapabilities)
	if err := ValidateAndNormalizeRtpCapabilities(&clonedRouterRtpCapabilities); err != nil {
		return err
	}

	clonedNativeRecvRtpCapabilities, err := d.nativeRtpCapabilities(RTPDirectionRecvOnly)
	if err != nil {
		return fmt.Errorf("get native receiving RTP capabilities: %w", err)
	}
	clonedNativeRecvRtpCapabilities = cloneRtpCapabilities(clonedNativeRecvRtpCapabilities)
	if err := ValidateAndNormalizeRtpCapabilities(&clonedNativeRecvRtpCapabilities); err != nil {
		return err
	}

	clonedNativeSendRtpCapabilities, err := d.nativeRtpCapabilities(RTPDirectionSendOnly)
	if err != nil {
		return fmt.Errorf("get native sending RTP capabilities: %w", err)
	}
	clonedNativeSendRtpCapabilities = cloneRtpCapabilities(clonedNativeSendRtpCapabilities)
	if err := ValidateAndNormalizeRtpCapabilities(&clonedNativeSendRtpCapabilities); err != nil {
		return err
	}

	d.getSendExtendedRtpCapabilities = func(nativeSendRtpCapabilities RtpCapabilities) ExtendedRtpCapabilities {
		return GetExtendedRtpCapabilities(
			cloneRtpCapabilities(nativeSendRtpCapabilities),
			clonedRouterRtpCapabilities,
			preferLocalCodecsOrder,
		)
	}

	recvExtendedRtpCapabilities := GetExtendedRtpCapabilities(
		clonedNativeRecvRtpCapabilities,
		clonedRouterRtpCapabilities,
		false,
	)
	d.recvRtpCapabilities = GetRecvRtpCapabilities(recvExtendedRtpCapabilities)
	if err := ValidateAndNormalizeRtpCapabilities(&d.recvRtpCapabilities); err != nil {
		return err
	}

	sendExtendedRtpCapabilities := GetExtendedRtpCapabilities(
		clonedNativeSendRtpCapabilities,
		clonedRouterRtpCapabilities,
		preferLocalCodecsOrder,
	)
	d.sendRtpCapabilities = GetSendRtpCapabilities(sendExtendedRtpCapabilities)
	if err := ValidateAndNormalizeRtpCapabilities(&d.sendRtpCapabilities); err != nil {
		return err
	}

	d.canProduceByKind[MediaKindAudio] = CanSend(MediaKindAudio, d.sendRtpCapabilities)
	d.canProduceByKind[MediaKindVideo] = CanSend(MediaKindVideo, d.sendRtpCapabilities)

	d.sctpCapabilities, err = d.nativeSctpCapabilities()
	if err != nil {
		return fmt.Errorf("get native SCTP capabilities: %w", err)
	}
	if err := ValidateSctpCapabilities(&d.sctpCapabilities); err != nil {
		return err
	}

	d.loaded = true

	return nil
}

// RecvRtpCapabilities returns the loaded receiving RTP capabilities.
func (d *Device) RecvRtpCapabilities() (RtpCapabilities, error) {
	if !d.loaded {
		return RtpCapabilities{}, errors.New("not loaded")
	}
	return cloneRtpCapabilities(d.recvRtpCapabilities), nil
}

// SendRtpCapabilities returns the loaded sending RTP capabilities.
func (d *Device) SendRtpCapabilities() (RtpCapabilities, error) {
	if !d.loaded {
		return RtpCapabilities{}, errors.New("not loaded")
	}
	return cloneRtpCapabilities(d.sendRtpCapabilities), nil
}

// SctpCapabilities returns the loaded SCTP capabilities.
func (d *Device) SctpCapabilities() (SctpCapabilities, error) {
	if !d.loaded {
		return SctpCapabilities{}, errors.New("not loaded")
	}
	return d.sctpCapabilities, nil
}

// CanProduce mirrors mediasoup-client Device.canProduce().
func (d *Device) CanProduce(kind MediaKind) (bool, error) {
	if !d.loaded {
		return false, errors.New("not loaded")
	}
	if kind != MediaKindAudio && kind != MediaKindVideo {
		return false, fmt.Errorf("invalid kind %q", kind)
	}
	return d.canProduceByKind[kind], nil
}

// CreateSendTransport mirrors mediasoup-client Device.createSendTransport().
func (d *Device) CreateSendTransport(options SendTransportOptions) (*SendTransport, error) {
	if !d.loaded {
		return nil, errors.New("not loaded")
	}

	core, err := newTransportCore(transportCoreConfig{
		api:                            d.api,
		direction:                      transportDirectionSend,
		baseOptions:                    options.BaseTransportOptions,
		getSendExtendedRtpCapabilities: d.getSendExtendedRtpCapabilities,
		nativeRtpCapabilities:          d.nativeRtpCapabilities,
		recvRtpCapabilities:            d.recvRtpCapabilities,
		canProduceByKind:               d.canProduceByKind,
	})
	if err != nil {
		return nil, err
	}

	return &SendTransport{
		core:          core,
		onProduce:     options.OnProduce,
		onProduceData: options.OnProduceData,
	}, nil
}

// CreateRecvTransport mirrors mediasoup-client Device.createRecvTransport().
func (d *Device) CreateRecvTransport(options RecvTransportOptions) (*RecvTransport, error) {
	if !d.loaded {
		return nil, errors.New("not loaded")
	}

	core, err := newTransportCore(transportCoreConfig{
		api:                            d.api,
		direction:                      transportDirectionRecv,
		baseOptions:                    options.BaseTransportOptions,
		getSendExtendedRtpCapabilities: d.getSendExtendedRtpCapabilities,
		nativeRtpCapabilities:          d.nativeRtpCapabilities,
		recvRtpCapabilities:            d.recvRtpCapabilities,
		canProduceByKind:               d.canProduceByKind,
	})
	if err != nil {
		return nil, err
	}

	return &RecvTransport{core: core}, nil
}

func newDefaultPionAPI() (*webrtc.API, error) {
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterDefaultCodecs(); err != nil {
		return nil, fmt.Errorf("register default codecs: %w", err)
	}

	interceptorRegistry := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(mediaEngine, interceptorRegistry); err != nil {
		return nil, fmt.Errorf("register default interceptors: %w", err)
	}

	return webrtc.NewAPI(
		webrtc.WithMediaEngine(mediaEngine),
		webrtc.WithInterceptorRegistry(interceptorRegistry),
	), nil
}
