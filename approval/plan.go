package approval

type Plan struct {
	Required bool    `json:"required"`
	Request  Request `json:"request,omitempty"`
}

func RequiredPlan(req Request) Plan {
	return Plan{Required: true, Request: req}
}

func (p Plan) Validate() error {
	if !p.Required {
		return nil
	}
	return p.Request.Validate()
}
