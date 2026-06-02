package session

import (
	"errors"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/models"
)

func TestValidateSSHTunnel(t *testing.T) {
	tests := []struct {
		name    string
		tunnel  *models.SSHTunnelConfig
		wantErr error
	}{
		{
			name:    "nil tunnel is valid",
			tunnel:  nil,
			wantErr: nil,
		},
		{
			name:    "empty host rejected",
			tunnel:  &models.SSHTunnelConfig{Host: "", User: "ops"},
			wantErr: ErrSSHTunnelHostMissing,
		},
		{
			name:    "empty user rejected",
			tunnel:  &models.SSHTunnelConfig{Host: "bastion", User: ""},
			wantErr: ErrSSHTunnelUserMissing,
		},
		{
			name: "fully populated is valid",
			tunnel: &models.SSHTunnelConfig{
				Host:              "bastion",
				User:              "ops",
				Port:              2222,
				IdentityFile:      "/id",
				IdentityFromAgent: true,
				PassphraseCommand: "pass show key",
				KnownHosts:        "/home/u/.ssh/known_hosts",
			},
			wantErr: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSSHTunnel(tt.tunnel)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("ValidateSSHTunnel() err = %v; want %v", err, tt.wantErr)
			}
		})
	}
}

func TestSSHTunnelPort(t *testing.T) {
	tests := []struct {
		name   string
		tunnel *models.SSHTunnelConfig
		want   int
	}{
		{"nil tunnel defaults to 22", nil, 22},
		{"zero port defaults to 22", &models.SSHTunnelConfig{Host: "h", User: "u", Port: 0}, 22},
		{"explicit port preserved", &models.SSHTunnelConfig{Host: "h", User: "u", Port: 2222}, 2222},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SSHTunnelPort(tt.tunnel); got != tt.want {
				t.Errorf("SSHTunnelPort() = %d; want %d", got, tt.want)
			}
		})
	}
}
