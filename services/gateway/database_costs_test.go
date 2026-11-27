package main

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	ct "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

type costAPIFixture struct {
	active   bool
	queries  []*costexplorer.GetCostAndUsageInput
	pages    []*costexplorer.GetCostAndUsageOutput
	tagCalls int
	tagCycle bool
}

func (f *costAPIFixture) ListCostAllocationTags(_ context.Context, in *costexplorer.ListCostAllocationTagsInput, _ ...func(*costexplorer.Options)) (*costexplorer.ListCostAllocationTagsOutput, error) {
	f.tagCalls++
	status := ct.CostAllocationTagStatusInactive
	if f.active {
		status = ct.CostAllocationTagStatusActive
	}
	out := &costexplorer.ListCostAllocationTagsOutput{CostAllocationTags: []ct.CostAllocationTag{
		{TagKey: aws.String(databaseTenantTag), Status: status}, {TagKey: aws.String(databaseScopeTag), Status: status},
	}}
	if f.tagCycle {
		cursor := "a"
		if aws.ToString(in.NextToken) == "a" {
			cursor = "b"
		}
		out.NextToken = aws.String(cursor)
	}
	return out, nil
}
func (f *costAPIFixture) GetDimensionValues(context.Context, *costexplorer.GetDimensionValuesInput, ...func(*costexplorer.Options)) (*costexplorer.GetDimensionValuesOutput, error) {
	return &costexplorer.GetDimensionValuesOutput{DimensionValues: []ct.DimensionValuesWithAttributes{{Value: aws.String("Amazon DynamoDB")}}}, nil
}
func (f *costAPIFixture) GetCostAndUsage(_ context.Context, in *costexplorer.GetCostAndUsageInput, _ ...func(*costexplorer.Options)) (*costexplorer.GetCostAndUsageOutput, error) {
	f.queries = append(f.queries, in)
	i := len(f.queries) - 1
	if i >= len(f.pages) {
		i = len(f.pages) - 1
	}
	return f.pages[i], nil
}
func costPage(amount string) *costexplorer.GetCostAndUsageOutput {
	return &costexplorer.GetCostAndUsageOutput{ResultsByTime: []ct.ResultByTime{{
		TimePeriod: &ct.DateInterval{Start: aws.String("2026-10-01"), End: aws.String("2026-11-01")},
		Groups:     []ct.Group{{Keys: []string{"CookistTenant$alpha"}, Metrics: map[string]ct.MetricValue{"NetUnblendedCost": {Amount: aws.String(amount), Unit: aws.String("USD")}}}},
	}}}
}

func TestDatabaseCostDecimalMarkupRoundingAndOverflow(t *testing.T) {
	for _, test := range []struct {
		raw   string
		cents int64
	}{
		{"20", 3800}, {"30", 5700}, {"0", 0}, {"0.0026", 0}, {"0.0027", 1}, {"0.05", 10},
		{"-0.05", -10}, {"-20", -3800}, {"20.0000000000000000001", 3800},
	} {
		got, err := databaseChargeCents(test.raw)
		if err != nil || got != test.cents {
			t.Fatalf("%s: %d, %v; want %d", test.raw, got, err, test.cents)
		}
	}
	for _, raw := range []string{"", "1e2", "NaN", " 20", "20 USD", "9999999999999999999999999999"} {
		if _, err := databaseChargeCents(raw); err == nil {
			t.Fatalf("accepted invalid/overflowing %q", raw)
		}
	}
}

func TestAWSDatabaseReportScopesActualCostsAndAggregatesBeforeRounding(t *testing.T) {
	page1, page2 := costPage("10.0001"), costPage("9.9999")
	page1.NextPageToken = aws.String("next")
	page2.ResultsByTime[0].Groups = append(page2.ResultsByTime[0].Groups, ct.Group{Keys: []string{"CookistTenant$beta"},
		Metrics: map[string]ct.MetricValue{"NetUnblendedCost": {Amount: aws.String("30"), Unit: aws.String("USD")}}})
	f := &costAPIFixture{active: true, pages: []*costexplorer.GetCostAndUsageOutput{page1, page2}}
	source := &awsDatabaseCosts{client: f, account: "123456789012", scope: "data-test"}
	report, err := source.Report(context.Background(), "2026-10", usageTestNow)
	if err != nil || !reflect.DeepEqual(report.Costs, map[string]string{"alpha": "20", "beta": "30"}) {
		t.Fatalf("%+v %v", report, err)
	}
	query := f.queries[0]
	if query.Granularity != ct.GranularityMonthly || !reflect.DeepEqual(query.Metrics, []string{"NetUnblendedCost"}) ||
		len(query.GroupBy) != 1 || aws.ToString(query.GroupBy[0].Key) != databaseTenantTag ||
		query.Filter.And[0].Dimensions.Values[0] != "Amazon DynamoDB" ||
		query.Filter.And[1].Dimensions.Values[0] != "123456789012" ||
		aws.ToString(query.Filter.And[2].Tags.Key) != databaseScopeTag || query.Filter.And[2].Tags.Values[0] != "data-test" ||
		aws.ToString(f.queries[1].NextPageToken) != "next" {
		t.Fatal("AWS query is not scoped to actual tagged tenant database costs")
	}
}

func TestAWSDatabaseReportRejectsUnsettledEstimatedUnattributedAndInvalidData(t *testing.T) {
	for _, kind := range []string{"estimated", "period", "currency", "metric", "amount", "tenant", "cursor", "inactive", "empty", "settlement"} {
		t.Run(kind, func(t *testing.T) {
			page := costPage("20")
			f := &costAPIFixture{active: true, pages: []*costexplorer.GetCostAndUsageOutput{page}}
			now := usageTestNow
			switch kind {
			case "estimated":
				page.ResultsByTime[0].Estimated = true
			case "period":
				page.ResultsByTime[0].TimePeriod.End = aws.String("2026-11-02")
			case "currency":
				page.ResultsByTime[0].Groups[0].Metrics["NetUnblendedCost"] = ct.MetricValue{Amount: aws.String("20"), Unit: aws.String("EUR")}
			case "metric":
				page.ResultsByTime[0].Groups[0].Metrics = map[string]ct.MetricValue{}
			case "amount":
				page.ResultsByTime[0].Groups[0].Metrics["NetUnblendedCost"] = ct.MetricValue{Amount: aws.String("NaN"), Unit: aws.String("USD")}
			case "tenant":
				page.ResultsByTime[0].Groups[0].Keys = []string{"CookistTenant$"}
			case "cursor":
				page.NextPageToken = aws.String("repeat")
			case "inactive":
				f.active = false
			case "empty":
				page.ResultsByTime = nil
			case "settlement":
				now = time.Date(2026, 11, 7, 23, 59, 59, 0, time.UTC)
			}
			source := &awsDatabaseCosts{client: f, account: "123456789012", scope: "data-test"}
			if _, err := source.Report(context.Background(), "2026-10", now); err == nil {
				t.Fatal("unsafe report accepted")
			}
		})
	}
	f := &costAPIFixture{active: true, tagCycle: true}
	if err := (&awsDatabaseCosts{client: f}).Ready(context.Background()); err == nil || f.tagCalls != 3 {
		t.Fatal("multi-page tag cursor cycle was not rejected")
	}
	if _, _, err := databaseCostPeriod("2026-10", time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal("exact settlement boundary rejected")
	}
}
