package publicsettings

import (
	"context"
	"fmt"

	"juhe-ai/backend-go/internal/store/port"
)

type Service struct {
	reader port.PublicSettingsReader
}

type Response struct {
	AppName string `json:"appName"`
	AppIcon string `json:"appIcon"`
}

func NewService(reader port.PublicSettingsReader) Service {
	return Service{reader: reader}
}

func (s Service) Get(ctx context.Context) (Response, error) {
	if s.reader == nil {
		return Response{}, fmt.Errorf("public settings reader is required")
	}
	settings, err := s.reader.PublicGlobalSettings(ctx)
	if err != nil {
		return Response{}, err
	}
	return Response{
		AppName: settings.AppName,
		AppIcon: settings.AppIcon,
	}, nil
}
