// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/mattermost/mattermost-server/v5/mlog"
	"github.com/mattermost/mattermost-server/v5/model"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/credentials/ec2rolecreds"
	"github.com/aws/aws-sdk-go/aws/ec2metadata"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/marketplacemetering"
	"github.com/aws/aws-sdk-go/service/marketplacemetering/marketplacemeteringiface"
)

type AWSMeterReport struct {
	Dimension string    `json:"dimension"`
	Value     int64     `json:"value"`
	Timestamp time.Time `json:"timestamp"`
}

func (o *AWSMeterReport) ToJSON() string {
	b, _ := json.Marshal(o)
	return string(b)
}

type AWSMeterService struct {
	AwsDryRun      bool
	AwsProductCode string
	AwsMeteringSvc marketplacemeteringiface.MarketplaceMeteringAPI
}

func (a *App) NewAWSMeterService() (*AWSMeterService, error) {
	svc := &AWSMeterService{
		AwsDryRun:      true,
		AwsProductCode: "12345",
	}

	service, err := NewAWSMarketplaceMeteringService()

	mlog.Info("CITOMAI::NewAWSMeterService1")

	if err != nil {
		mlog.Error("NewAWSMarketplaceMeteringService", mlog.String("error", err.Error()))
		return nil, err
	}

	mlog.Info("CITOMAI::NewAWSMeterService2")

	svc.AwsMeteringSvc = service
	return svc, nil
}

func NewAWSMarketplaceMeteringService() (*marketplacemetering.MarketplaceMetering, error) {
	region := os.Getenv("AWS_REGION")
	s := session.Must(session.NewSession(&aws.Config{Region: &region}))

	creds := credentials.NewChainCredentials(
		[]credentials.Provider{
			&ec2rolecreds.EC2RoleProvider{
				Client: ec2metadata.New(s),
			},
		})

	credsValue, err := creds.Get()
	if err != nil {
		mlog.Error("session is invalid", mlog.String("error", err.Error()))
		return nil, errors.New("cannot obtain credentials")
	}

	mlog.Info("CITOMAI::NewAWSMarketplaceMeteringService", mlog.Any("credentials", credsValue))

	return marketplacemetering.New(session.Must(session.NewSession(&aws.Config{
		Credentials: creds,
	}))), nil
}

// a report entry is for all metrics
func (a *App) GetUserCategoryUsage(dimensions []string, startTime time.Time, endTime time.Time) []*AWSMeterReport {
	reports := make([]*AWSMeterReport, 0)

	mlog.Info("CITOMAI::GetUserCategoryUsage", mlog.String("startTime", startTime.String()))

	for _, dimension := range dimensions {
		var userCount int64
		var appErr *model.AppError

		mlog.Info("CITOMAI::GetUserCategoryUsage", mlog.String("dimension", dimension))

		switch dimension {
		case "UsageHrs":
			userCount, appErr = a.Srv().Store.User().AnalyticsActiveCountForPeriod(model.GetMillisForTime(startTime), model.GetMillisForTime(endTime), model.UserCountOptions{})

			if appErr != nil {
				mlog.Error("Failed to obtain usage data", mlog.String("dimension", dimension), mlog.String("start", startTime.String()), mlog.Int64("count", userCount))
			}
			mlog.Info("CITOMAI::GetUserCategoryUsage", mlog.Int64("user count", userCount))
		default:
			mlog.Error("Dimension does not exist!", mlog.String("dimension", dimension))
		}

		if appErr != nil {
			mlog.Error("Failed to obtain usage.", mlog.String("dimension", dimension))
			return reports
		}

		report := &AWSMeterReport{
			Dimension: dimension,
			Value:     userCount,
			Timestamp: startTime,
		}

		mlog.Info("CITOMAI::GetUserCategoryUsage", mlog.String("report", report.ToJSON()))

		reports = append(reports, report)
	}

	return reports
}

func (a *App) ReportUserCategoryUsage(ams *AWSMeterService, reports []*AWSMeterReport) *model.AppError {
	for _, report := range reports {
		mlog.Info("CITOMAI::ReportUserCategoryUsage", mlog.String("report", report.ToJSON()))
		err := a.SendReportToMeteringService(ams, report)
		if err != nil {
			return err
		}
	}
	return nil
}

func (a *App) SendReportToMeteringService(ams *AWSMeterService, report *AWSMeterReport) *model.AppError {
	params := &marketplacemetering.MeterUsageInput{
		DryRun:         aws.Bool(ams.AwsDryRun),
		ProductCode:    aws.String(ams.AwsProductCode),
		UsageDimension: aws.String(report.Dimension),
		UsageQuantity:  aws.Int64(report.Value),
		Timestamp:      aws.Time(report.Timestamp),
	}

	mlog.Debug("CITOMAI::SendReportToMeteringService", mlog.String("params", params.GoString()))

	resp, err := ams.AwsMeteringSvc.MeterUsage(params)
	if err != nil {
		mlog.Error("CITOMAI::SendReportToMeteringService", mlog.String("error", err.Error()))
		return model.NewAppError("ReportCategoryDimension", "MeteringRecordId is invalid", nil, err.Error(), http.StatusNotFound)
	}
	if resp.MeteringRecordId == nil {
		mlog.Error("CITOMAI::SendReportToMeteringService", mlog.String("error", "MeteringRecordId is invalid"))
		return model.NewAppError("ReportCategoryDimension", "MeteringRecordId is invalid", nil, "", http.StatusNotFound)
	}

	mlog.Debug("Sent record to AWS metering service", mlog.String("dimension", report.Dimension), mlog.Int64("value", report.Value), mlog.String("timestamp", report.Timestamp.String()))

	return nil
}
