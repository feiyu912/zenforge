package middleware

import (
	"context"

	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/tool"
)

type ApprovalConfig struct {
	Broker approval.Broker
}

func Approval(config ApprovalConfig) tool.Middleware {
	return func(next tool.Invoker) tool.Invoker {
		return tool.InvokerFunc(func(ctx context.Context, call tool.Call) (tool.Result, error) {
			result, err := next.Invoke(ctx, call)
			req, ok := approval.RequestFromResult(result)
			if !ok {
				return result, err
			}
			req = approval.BindRequest(req, call.RunID, call.ID, call.Name)
			if validationErr := req.Validate(); validationErr != nil {
				return tool.Result{Error: validationErr.Error(), ExitCode: 1}, validationErr
			}
			if config.Broker == nil {
				return approval.RequiredResult(req), approval.ErrRequired
			}
			decision, decisionErr := config.Broker.Request(ctx, req)
			if decisionErr != nil {
				return tool.Result{Error: decisionErr.Error(), ExitCode: 1}, decisionErr
			}
			if err := approval.ValidateDecisionForRequest(req, decision); err != nil {
				return tool.Result{Error: err.Error(), ExitCode: 1}, err
			}
			switch decision.Action {
			case approval.DecisionApprove, approval.DecisionAlways:
				approvedCall := call
				approvedCall.Metadata = approval.ApprovedMetadata(call.Metadata, req, decision)
				return next.Invoke(ctx, approvedCall)
			case approval.DecisionAbort:
				err := approval.NewAbortError(decision.Reason)
				return tool.Result{Error: err.Error(), ExitCode: 1}, err
			default:
				errorCode := approval.ErrorRejected
				if decision.Reason == approval.ErrorExpired {
					errorCode = approval.ErrorExpired
				}
				return tool.Result{Error: errorCode, ExitCode: 1, Structured: map[string]any{
					"approval": req,
					"decision": decision,
				}}, nil
			}
		})
	}
}
