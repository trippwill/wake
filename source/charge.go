// SPDX-FileCopyrightText: 2026 trippwill
// SPDX-License-Identifier: MPL-2.0

package source

import (
	"net/url"
	"slices"
	"strings"

	"go.trippwill.dev/wake/financial"
)

type ChargeCategory string

const (
	Commission             ChargeCategory = "commission"
	BrokerExecutionFee     ChargeCategory = "broker-execution-fee"
	BrokerClearingFee      ChargeCategory = "broker-clearing-fee"
	ThirdPartyExecutionFee ChargeCategory = "third-party-execution-fee"
	ThirdPartyClearingFee  ChargeCategory = "third-party-clearing-fee"
	RegulatoryFee          ChargeCategory = "regulatory-fee"
	TransactionFee         ChargeCategory = "transaction-fee"
	Fee                    ChargeCategory = "fee"
	Tax                    ChargeCategory = "tax"
	SalesTax               ChargeCategory = "sales-tax"
	OtherTax               ChargeCategory = "other-tax"
	OtherFee               ChargeCategory = "other-fee"
)

// ChargeRole identifies a source's monetary role independently of its amount.
type ChargeRole struct {
	category ChargeCategory
	field    string
}

func NewChargeRole(category ChargeCategory, field string) (ChargeRole, error) {
	if !validText(field, 128) {
		return ChargeRole{}, ErrInvalidTrade
	}

	switch category {
	case Commission, BrokerExecutionFee, BrokerClearingFee, ThirdPartyExecutionFee,
		ThirdPartyClearingFee, RegulatoryFee, TransactionFee, Fee, Tax, SalesTax, OtherTax, OtherFee:
	default:
		return ChargeRole{}, ErrInvalidTrade
	}

	return ChargeRole{category: category, field: field}, nil
}

func (r ChargeRole) Category() ChargeCategory { return r.category }
func (r ChargeRole) Field() string            { return r.field }

type ChargeInput struct {
	Category   ChargeCategory
	Field      string
	Total      financial.Money
	Components []Charge
}

// Charge preserves a reported total and its independently reported breakdown.
// A missing total is not a sum of the components.
type Charge struct {
	role       ChargeRole
	total      financial.Money
	components []Charge
}

func NewCharge(f ChargeInput) (Charge, error) {
	role, err := NewChargeRole(f.Category, f.Field)
	if err != nil || (f.Total.Amount() == "" && len(f.Components) == 0) || !validCharges(f.Components) {
		return Charge{}, ErrInvalidTrade
	}

	v := Charge{role: role, total: f.Total, components: slices.Clone(f.Components)}
	if chargeCount([]Charge{v}) > 32 {
		return Charge{}, ErrInvalidTrade
	}

	return v, nil
}

func (c Charge) Role() ChargeRole         { return c.role }
func (c Charge) Category() ChargeCategory { return c.role.Category() }
func (c Charge) Field() string            { return c.role.Field() }
func (c Charge) Total() financial.Money   { return c.total }
func (c Charge) Components() []Charge     { return slices.Clone(c.components) }

func chargeCount(values []Charge) int {
	n := len(values)
	for _, c := range values {
		n += chargeCount(c.components)
	}

	return n
}

func validCharges(values []Charge) bool {
	if chargeCount(values) > 32 {
		return false
	}

	seen := map[string]bool{}
	for _, c := range values {
		if c.Field() == "" || seen[c.Field()] {
			return false
		}

		seen[c.Field()] = true
	}

	return true
}

func chargePath(parents []ChargeRole, role ChargeRole) string {
	parts := make([]string, 0, len(parents)+1)
	for _, parent := range parents {
		parts = append(parts, url.PathEscape(parent.Field()))
	}

	parts = append(parts, url.PathEscape(role.Field()))

	return strings.Join(parts, "/")
}

// ChargeFields keeps every reported monetary assertion independently selectable.
func ChargeFields(values []Charge) []ActivityField {
	var (
		fields []ActivityField
		visit  func([]Charge, []ChargeRole)
	)

	visit = func(values []Charge, parents []ChargeRole) {
		for _, c := range values {
			if c.Total().Amount() != "" {
				leaf, _ := NewCharge(ChargeInput{Category: c.Category(), Field: c.Field(), Total: c.Total()})
				field, _ := NewActivityField(
					ActivityFieldInput{Name: FieldCharge, Charge: leaf, ChargeParents: parents},
				)
				fields = append(fields, field)
			}

			visit(c.components, append(slices.Clone(parents), c.Role()))
		}
	}
	visit(values, nil)

	return fields
}

// ChargesFromFields materializes selected assertions. Conflicting raw assertions
// remain valid observations but cannot become an accepted charge tree.
func ChargesFromFields(fields []ActivityField) ([]Charge, error) {
	type node struct {
		role     ChargeRole
		total    financial.Money
		children []*node
	}

	var roots []*node

	count := 0
	for _, field := range fields {
		if field.Name() != FieldCharge || !field.Known() {
			continue
		}

		f := field.Fields()

		path := append(slices.Clone(f.ChargeParents), f.Charge.Role())
		siblings := &roots

		var selected *node
		for _, role := range path {
			selected = nil
			for _, old := range *siblings {
				if old.role.Field() == role.Field() {
					selected = old
					break
				}
			}

			if selected == nil {
				count++
				if count > 32 {
					return nil, ErrInvalidTrade
				}

				selected = &node{role: role}
				*siblings = append(*siblings, selected)
			} else if selected.role != role {
				return nil, ErrInvalidTrade
			}

			siblings = &selected.children
		}

		if selected.total.Amount() != "" && !selected.total.Equal(f.Charge.Total()) {
			return nil, ErrInvalidTrade
		}

		if selected.total.Amount() == "" {
			selected.total = f.Charge.Total()
		}
	}

	var build func([]*node) ([]Charge, error)

	build = func(nodes []*node) ([]Charge, error) {
		var out []Charge
		for _, n := range nodes {
			children, err := build(n.children)
			if err != nil {
				return nil, err
			}

			c, err := NewCharge(
				ChargeInput{Category: n.role.Category(), Field: n.role.Field(), Total: n.total, Components: children},
			)
			if err != nil {
				return nil, err
			}

			out = append(out, c)
		}

		return out, nil
	}

	return build(roots)
}
