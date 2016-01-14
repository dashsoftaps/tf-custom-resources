package dashsoftaws

import (
	"net"
	"sync"
	"time"

	"github.com/hashicorp/terraform/helper/hashcode"
	"github.com/hashicorp/terraform/helper/mutexkv"
	"github.com/hashicorp/terraform/helper/schema"
	"github.com/hashicorp/terraform/terraform"

	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/credentials/ec2rolecreds"
	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	"github.com/aws/aws-sdk-go/aws/session"
)

// Provider returns a terraform.ResourceProvider.
func Provider() terraform.ResourceProvider {
	// TODO: Move the validation to this, requires conditional schemas
	// TODO: Move the configuration to this, requires validation

	// These variables are closed within the `getCreds` function below.
	// This function is responsible for reading credentials from the
	// environment in the case that they're not explicitly specified
	// in the Terraform configuration.
	//
	// By using the getCreds function here instead of making the default
	// empty, we avoid asking for input on credentials if they're available
	// in the environment.
	var credVal credentials.Value
	var credErr error
	var once sync.Once
	getCreds := func() {
		// Build the list of providers to look for creds in
		providers := []credentials.Provider{
			&credentials.EnvProvider{},
			&credentials.SharedCredentialsProvider{},
		}

		// We only look in the EC2 metadata API if we can connect
		// to the metadata service within a reasonable amount of time
		conn, err := net.DialTimeout("tcp", "169.254.169.254:80", 100*time.Millisecond)
		if err == nil {
			conn.Close()
			providers = append(providers, &ec2rolecreds.EC2RoleProvider{Client: ec2metadata.New(session.New())})
		}

		credVal, credErr = credentials.NewChainCredentials(providers).Get()

		// If we didn't successfully find any credentials, just
		// set the error to nil.
		if credErr == credentials.ErrNoValidProvidersFoundInChain {
			credErr = nil
		}
	}

	// getCredDefault is a function used by DefaultFunc below to
	// get the default value for various parts of the credentials.
	// This function properly handles loading the credentials, checking
	// for errors, etc.
	getCredDefault := func(def interface{}, f func() string) (interface{}, error) {
		once.Do(getCreds)

		// If there was an error, that is always first
		if credErr != nil {
			return nil, credErr
		}

		// If the value is empty string, return nil (not set)
		val := f()
		if val == "" {
			return def, nil
		}

		return val, nil
	}

	// The actual provider
	return &schema.Provider{
		Schema: map[string]*schema.Schema{
			"access_key": &schema.Schema{
				Type:     schema.TypeString,
				Required: true,
				DefaultFunc: func() (interface{}, error) {
					return getCredDefault(nil, func() string {
						return credVal.AccessKeyID
					})
				},
				Description: descriptions["access_key"],
			},

			"secret_key": &schema.Schema{
				Type:     schema.TypeString,
				Required: true,
				DefaultFunc: func() (interface{}, error) {
					return getCredDefault(nil, func() string {
						return credVal.SecretAccessKey
					})
				},
				Description: descriptions["secret_key"],
			},

			"token": &schema.Schema{
				Type:     schema.TypeString,
				Optional: true,
				DefaultFunc: func() (interface{}, error) {
					return getCredDefault("", func() string {
						return credVal.SessionToken
					})
				},
				Description: descriptions["token"],
			},

			"region": &schema.Schema{
				Type:     schema.TypeString,
				Required: true,
				DefaultFunc: schema.MultiEnvDefaultFunc([]string{
					"AWS_REGION",
					"AWS_DEFAULT_REGION",
				}, nil),
				Description:  descriptions["region"],
				InputDefault: "us-east-1",
			},

			"max_retries": &schema.Schema{
				Type:        schema.TypeInt,
				Optional:    true,
				Default:     11,
				Description: descriptions["max_retries"],
			},

			"allowed_account_ids": &schema.Schema{
				Type:          schema.TypeSet,
				Elem:          &schema.Schema{Type: schema.TypeString},
				Optional:      true,
				ConflictsWith: []string{"forbidden_account_ids"},
				Set: func(v interface{}) int {
					return hashcode.String(v.(string))
				},
			},

			"forbidden_account_ids": &schema.Schema{
				Type:          schema.TypeSet,
				Elem:          &schema.Schema{Type: schema.TypeString},
				Optional:      true,
				ConflictsWith: []string{"allowed_account_ids"},
				Set: func(v interface{}) int {
					return hashcode.String(v.(string))
				},
			},

			"dynamodb_endpoint": &schema.Schema{
				Type:        schema.TypeString,
				Optional:    true,
				Default:     "",
				Description: descriptions["dynamodb_endpoint"],
			},

			"kinesis_endpoint": &schema.Schema{
				Type:        schema.TypeString,
				Optional:    true,
				Default:     "",
				Description: descriptions["kinesis_endpoint"],
			},
		},

		ResourcesMap: map[string]*schema.Resource{
			"dashsoftaws_dynamodb_table": resourceDashsoftAwsDynamodbTable(),
		},

		ConfigureFunc: providerConfigure,
	}
}

var descriptions map[string]string

func init() {
	descriptions = map[string]string{
		"region": "The region where AWS operations will take place. Examples\n" +
			"are us-east-1, us-west-2, etc.",

		"access_key": "The access key for API operations. You can retrieve this\n" +
			"from the 'Security & Credentials' section of the AWS console.",

		"secret_key": "The secret key for API operations. You can retrieve this\n" +
			"from the 'Security & Credentials' section of the AWS console.",

		"token": "session token. A session token is only required if you are\n" +
			"using temporary security credentials.",

		"max_retries": "The maximum number of times an AWS API request is\n" +
			"being executed. If the API request still fails, an error is\n" +
			"thrown.",

		"dynamodb_endpoint": "Use this to override the default endpoint URL constructed from the `region`.\n" +
			"It's typically used to connect to dynamodb-local.",

		"kinesis_endpoint": "Use this to override the default endpoint URL constructed from the `region`.\n" +
			"It's typically used to connect to kinesalite.",
	}
}

func providerConfigure(d *schema.ResourceData) (interface{}, error) {
	config := Config{
		AccessKey:        d.Get("access_key").(string),
		SecretKey:        d.Get("secret_key").(string),
		Token:            d.Get("token").(string),
		Region:           d.Get("region").(string),
		MaxRetries:       d.Get("max_retries").(int),
		DynamoDBEndpoint: d.Get("dynamodb_endpoint").(string),
		KinesisEndpoint:  d.Get("kinesis_endpoint").(string),
	}

	if v, ok := d.GetOk("allowed_account_ids"); ok {
		config.AllowedAccountIds = v.(*schema.Set).List()
	}

	if v, ok := d.GetOk("forbidden_account_ids"); ok {
		config.ForbiddenAccountIds = v.(*schema.Set).List()
	}

	return config.Client()
}

// This is a global MutexKV for use within this plugin.
var awsMutexKV = mutexkv.NewMutexKV()