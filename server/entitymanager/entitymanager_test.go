package entitymanager

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/h-fam/errdiff"
	"github.com/openconfig/bootz/proto/bootz"
	"github.com/openconfig/bootz/server/entitymanager/proto/entity"
	"github.com/openconfig/bootz/server/service"
	"google.golang.org/protobuf/proto"
)

var chassis = entity.Chassis{
	Name:                   "test",
	SerialNumber:           "123",
	Manufacturer:           "Cisco",
	BootloaderPasswordHash: "ABCD123",
	BootMode:               bootz.BootMode_BOOT_MODE_INSECURE,
	Config: &entity.Config{
		BootConfig: &entity.BootConfig{},
		DhcpConfig: &entity.DHCPConfig{},
		GnsiConfig: &entity.GNSIConfig{},
	},
	SoftwareImage: &bootz.SoftwareImage{
		Name:          "Default Image",
		Version:       "1.0",
		Url:           "https://path/to/image",
		OsImageHash:   "ABCDEF",
		HashAlgorithm: "SHA256",
	},
	ControllerCards: []*entity.ControlCard{
		{
			SerialNumber:     "123A",
			OwnershipVoucher: "test_ov1",
		},
		{
			SerialNumber:     "123B",
			OwnershipVoucher: "test_ov2",
		},
	},
}

func TestNew(t *testing.T) {
	tests := []struct {
		desc        string
		chassisConf string
		inventory   map[service.EntityLookup]*entity.Chassis
		defaults    *entity.Options
		wantErr     string
	}{
		{
			desc:        "Successful new with file",
			chassisConf: "../../testdata/inventory.prototxt",
			inventory: map[service.EntityLookup]*entity.Chassis{{SerialNumber: chassis.SerialNumber,
				Manufacturer: chassis.Manufacturer}: &chassis},
			defaults: &entity.Options{
				Bootzserver: "bootzip:....",
				ArtifactDir: "../../testdata/",
			},
		},
		{
			desc:        "Unsuccessful new with wrong file",
			chassisConf: "../../testdata/wronginventory.prototxt",
			inventory:   map[service.EntityLookup]*entity.Chassis{},
			wantErr:     "proto:",
		},
		{
			desc:        "Unsuccessful new with wrong file path",
			chassisConf: "not/valid/path",
			inventory:   map[service.EntityLookup]*entity.Chassis{},
			wantErr:     "no such file or directory",
		},
		{
			desc:        "Successful new with empty file path",
			chassisConf: "",
			inventory:   map[service.EntityLookup]*entity.Chassis{},
		},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			inv, err := New(test.chassisConf)
			if err == nil {
				opts := []cmp.Option{
					cmpopts.IgnoreUnexported(entity.Chassis{}, entity.Options{}, bootz.SoftwareImage{}, entity.DHCPConfig{}, entity.GNSIConfig{}, entity.BootConfig{}, entity.Config{}, entity.BootConfig{}, entity.ControlCard{}, service.EntityLookup{}),
				}
				if !cmp.Equal(inv.chassisInventory, test.inventory, opts...) {
					t.Errorf("Inventory list is not as expected, Diff: %s", cmp.Diff(inv.chassisInventory, test.inventory, opts...))
				}
				if !cmp.Equal(inv.defaults, test.defaults, opts...) {
					t.Errorf("Inventory list is not as expected, Diff: %s", cmp.Diff(inv.defaults, test.defaults, opts...))
				}
			}
			fmt.Printf("err: %v", err)
			if s := errdiff.Substring(err, test.wantErr); s != "" {
				t.Errorf("Expected error %s, but got error %v", test.wantErr, err)
			}
		})
	}

}

func TestFetchOwnershipVoucher(t *testing.T) {
	tests := []struct {
		desc    string
		serial  string
		want    string
		wantErr bool
	}{{
		desc:    "Missing OV",
		serial:  "MissingSerial",
		wantErr: true,
	}, {
		desc:    "Found OV",
		serial:  "123A",
		want:    "test_ov1",
		wantErr: false,
	}}

	em, _ := New("")

	em.chassisInventory[service.EntityLookup{Manufacturer: "Cisco", SerialNumber: "123"}] = &chassis

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			got, err := em.FetchOwnershipVoucher(&service.EntityLookup{Manufacturer: "Cisco", SerialNumber: "123"}, test.serial)
			if (err != nil) != test.wantErr {
				t.Fatalf("FetchOwnershipVoucher(%v) err = %v, want %v", test.serial, err, test.wantErr)
			}
			if !cmp.Equal(got, test.want) {
				t.Errorf("FetchOwnershipVoucher(%v) got %v, want %v", test.serial, got, test.want)
			}
		})
	}
}

func TestResolveChassis(t *testing.T) {
	tests := []struct {
		desc    string
		input   *service.EntityLookup
		want    *service.ChassisEntity
		wantErr bool
	}{{
		desc: "Default device",
		input: &service.EntityLookup{
			SerialNumber: "123",
			Manufacturer: "Cisco",
		},
		want: &service.ChassisEntity{
			BootMode: bootz.BootMode_BOOT_MODE_SECURE,
		},
	}, {
		desc: "Chassis Not Found",
		input: &service.EntityLookup{
			SerialNumber: "456",
			Manufacturer: "Cisco",
		},
		want:    nil,
		wantErr: true,
	},
	}
	em, _ := New("")
	em.AddChassis(bootz.BootMode_BOOT_MODE_SECURE, "Cisco", "123")

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			got, err := em.ResolveChassis(test.input)
			if (err != nil) != test.wantErr {
				t.Fatalf("ResolveChassis(%v) err = %v, want %v", test.input, err, test.wantErr)
			}
			if !cmp.Equal(got, test.want) {
				t.Errorf("ResolveChassis(%v) got %v, want %v", test.input, got, test.want)
			}
		})
	}
}

func TestSign(t *testing.T) {
	tests := []struct {
		desc    string
		chassis service.EntityLookup
		serial  string
		resp    *bootz.GetBootstrapDataResponse
		wantOV  string
		wantOC  bool
		wantErr bool
	}{{
		desc: "Success",
		chassis: service.EntityLookup{
			Manufacturer: "Cisco",
			SerialNumber: "123",
		},
		serial: "123A",
		resp: &bootz.GetBootstrapDataResponse{
			SignedResponse: &bootz.BootstrapDataSigned{
				Responses: []*bootz.BootstrapDataResponse{
					{SerialNum: "123A"},
				},
			},
		},
		wantOV:  "test_ov1",
		wantOC:  true,
		wantErr: false,
	}, {
		desc:    "Empty response",
		resp:    &bootz.GetBootstrapDataResponse{},
		wantErr: true,
	},
	}

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {

			em, _ := New("../../testdata/inventory.prototxt")
			artifacts, err := parseSecurityArtifacts(em.defaults.GetArtifactDir())
			if err != nil {
				t.Errorf("Could not load security artifacts: %v", err)
			}

			err = em.Sign(test.resp, &test.chassis, test.serial)
			if err != nil {
				if test.wantErr {
					t.Skip()
				}
				t.Errorf("Sign() err = %v, want %v", err, test.wantErr)
			}
			signedResponseBytes, err := proto.Marshal(test.resp.GetSignedResponse())
			if err != nil {
				t.Fatal(err)
			}
			hashed := sha256.Sum256(signedResponseBytes)
			sigDecoded, err := base64.StdEncoding.DecodeString(test.resp.GetResponseSignature())
			if err != nil {
				t.Fatal(err)
			}

			block, _ := pem.Decode([]byte(artifacts.OC.Key))
			if block == nil {
				t.Fatal("unable to decode OC private key")
			}
			priv, err := x509.ParsePKCS1PrivateKey(block.Bytes)
			if err != nil {
				t.Errorf("unable to parse OC private key")
			}

			err = rsa.VerifyPKCS1v15(&priv.PublicKey, crypto.SHA256, hashed[:], sigDecoded)
			if err != nil {
				t.Errorf("Sign() err == %v, want %v", err, test.wantErr)
			}
			if gotOV, wantOV := string(test.resp.GetOwnershipVoucher()), test.wantOV; gotOV != wantOV {
				t.Errorf("Sign() ov = %v, want %v", gotOV, wantOV)
			}
			if test.wantOC {
				if gotOC, wantOC := string(test.resp.GetOwnershipCertificate()), artifacts.OC.Cert; gotOC != wantOC {
					t.Errorf("Sign() oc = %v, want %v", gotOC, wantOC)
				}
			}
		})
	}
}

func TestSetStatus(t *testing.T) {
	tests := []struct {
		desc    string
		input   *bootz.ReportStatusRequest
		wantErr bool
	}{{
		desc: "No control card states",
		input: &bootz.ReportStatusRequest{
			Status:        bootz.ReportStatusRequest_BOOTSTRAP_STATUS_SUCCESS,
			StatusMessage: "Bootstrap status succeeded",
		},
		wantErr: true,
	}, {
		desc: "Control card initialized",
		input: &bootz.ReportStatusRequest{
			Status:        bootz.ReportStatusRequest_BOOTSTRAP_STATUS_SUCCESS,
			StatusMessage: "Bootstrap status succeeded",
			States: []*bootz.ControlCardState{
				{
					SerialNumber: "123A",
					Status:       *bootz.ControlCardState_CONTROL_CARD_STATUS_INITIALIZED.Enum(),
				},
			},
		},
		wantErr: false,
	}, {
		desc: "Unknown control card",
		input: &bootz.ReportStatusRequest{
			Status:        bootz.ReportStatusRequest_BOOTSTRAP_STATUS_SUCCESS,
			StatusMessage: "Bootstrap status succeeded",
			States: []*bootz.ControlCardState{
				{
					SerialNumber: "123C",
					Status:       *bootz.ControlCardState_CONTROL_CARD_STATUS_INITIALIZED.Enum(),
				},
			},
		},
		wantErr: true,
	},
	}
	em, _ := New("")
	em.AddChassis(bootz.BootMode_BOOT_MODE_SECURE, "Cisco", "123").AddControlCard("123A")

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			err := em.SetStatus(test.input)
			if (err != nil) != test.wantErr {
				t.Errorf("SetStatus(%v) err = %v, want %v", test.input, err, test.wantErr)
			}
		})
	}
}

func TestGetBootstrapData(t *testing.T) {
	tests := []struct {
		desc    string
		input   *bootz.ControlCard
		want    *bootz.BootstrapDataResponse
		wantErr bool
	}{{
		desc:    "No serial number",
		input:   &bootz.ControlCard{},
		wantErr: true,
	}, {
		desc: "Control card not found",
		input: &bootz.ControlCard{
			SerialNumber: "456A",
		},
		wantErr: true,
	}, {
		desc: "Successful bootstrap",
		input: &bootz.ControlCard{
			SerialNumber: "123A",
		},
		want: &bootz.BootstrapDataResponse{
			SerialNum: "123A",
			IntendedImage: &bootz.SoftwareImage{
				Name:          "Default Image",
				Version:       "1.0",
				Url:           "https://path/to/image",
				OsImageHash:   "ABCDEF",
				HashAlgorithm: "SHA256",
			},
			BootPasswordHash: "ABCD123",
			ServerTrustCert:  "FakeTLSCert",
			BootConfig: &bootz.BootConfig{
				VendorConfig: []byte(""),
				OcConfig:     []byte(""),
			},
			Credentials: &bootz.Credentials{},
		},
		wantErr: false,
	},
	}

	em, _ := New("")
	em.chassisInventory[service.EntityLookup{Manufacturer: "Cisco", SerialNumber: "123"}] = &chassis

	for _, test := range tests {
		t.Run(test.desc, func(t *testing.T) {
			got, err := em.GetBootstrapData(&service.EntityLookup{Manufacturer: "Cisco", SerialNumber: "123"}, test.input)
			if (err != nil) != test.wantErr {
				t.Errorf("GetBootstrapData(%v) err = %v, want %v", test.input, err, test.wantErr)
			}
			if !proto.Equal(got, test.want) {
				t.Errorf("GetBootstrapData(%v) \n got: %v, \n want: %v", test.input, got, test.want)
			}
		})
	}
}
