package main

import (
	"github.com/0glabs/0g-data-avail/common/aws"
	"github.com/0glabs/0g-data-avail/common/geth"
	"github.com/0glabs/0g-data-avail/common/logging"
	"github.com/0glabs/0g-data-avail/common/ratelimit"
	"github.com/0glabs/0g-data-avail/common/storage_node"
	"github.com/0glabs/0g-data-avail/disperser"
	"github.com/0glabs/0g-data-avail/disperser/apiserver"
	"github.com/0glabs/0g-data-avail/disperser/cmd/apiserver/flags"
	"github.com/0glabs/0g-data-avail/disperser/common/blobstore"
	"github.com/urfave/cli"
)

type Config struct {
	AwsClientConfig   aws.ClientConfig
	BlobstoreConfig   blobstore.Config
	ServerConfig      disperser.ServerConfig
	LoggerConfig      logging.Config
	MetricsConfig     disperser.MetricsConfig
	RatelimiterConfig ratelimit.Config
	RateConfig        apiserver.RateConfig
	StorageNodeConfig storage_node.ClientConfig
	EthClientConfig   geth.EthClientConfig
	EnableRatelimiter bool
	BucketTableName   string
	BucketStoreSize   int
}

func NewConfig(ctx *cli.Context) (Config, error) {

	ratelimiterConfig, err := ratelimit.ReadCLIConfig(ctx, flags.FlagPrefix)
	if err != nil {
		return Config{}, err
	}

	rateConfig, err := apiserver.ReadCLIConfig(ctx)
	if err != nil {
		return Config{}, err
	}

	config := Config{
		AwsClientConfig: aws.ReadClientConfig(ctx, flags.FlagPrefix),
		ServerConfig: disperser.ServerConfig{
			GrpcPort: ctx.GlobalString(flags.GrpcPortFlag.Name),
		},
		EthClientConfig: geth.ReadEthClientConfig(ctx),
		BlobstoreConfig: blobstore.Config{
			BucketName:            ctx.GlobalString(flags.S3BucketNameFlag.Name),
			TableName:             ctx.GlobalString(flags.DynamoDBTableNameFlag.Name),
			MetadataHashAsBlobKey: ctx.GlobalBool(flags.MetadataHashAsBlobKey.Name),
		},
		LoggerConfig: logging.ReadCLIConfig(ctx, flags.FlagPrefix),
		MetricsConfig: disperser.MetricsConfig{
			HTTPPort:      ctx.GlobalString(flags.MetricsHTTPPort.Name),
			EnableMetrics: ctx.GlobalBool(flags.EnableMetrics.Name),
		},
		RatelimiterConfig: ratelimiterConfig,
		RateConfig:        rateConfig,
		EnableRatelimiter: ctx.GlobalBool(flags.EnableRatelimiter.Name),
		BucketTableName:   ctx.GlobalString(flags.BucketTableName.Name),
		BucketStoreSize:   ctx.GlobalInt(flags.BucketStoreSize.Name),
		StorageNodeConfig: storage_node.ReadClientConfig(ctx, flags.FlagPrefix),
	}
	return config, nil
}
