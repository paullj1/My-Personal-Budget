module BudgetsHelper

  def expand?(params, budget_id)
    params.has_key? "budget_#{budget_id}" or params.has_key? "search_#{budget_id}"
  end

end
