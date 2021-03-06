class Budget < ApplicationRecord
  has_and_belongs_to_many :user, :join_table => :users_budgets
  has_many :transact, dependent: :destroy, inverse_of: :budget
	validates :name,  presence: true, length: { maximum: 50 }
	validates :payroll,  presence: true, numericality: { greater_than_or_equal_to: 0 }

  def run_payroll
    @transact = Transact.new(description: "PAYROLL", credit: true, budget_id: self.id, amount: self.payroll)
    @transact.user_id = self.user.first

    if @transact.save
      # Update last run
      self.payroll_run_at = Time.zone.now
      self.save
    end
  end

  def transactions_this_month
    self.transact.where("created_at > ?", Time.zone.now.at_beginning_of_month).order(:created_at)
  end

  def credits_this_month
    self.transact.where("credit = ? created_at > ?", true, Time.zone.now.at_beginning_of_month).sum(:amount)
  end

  def debits_this_month
    self.transact.where("credit = ? AND created_at > ?", false, Time.zone.now.at_beginning_of_month).sum(:amount)
  end

  def all_transactions
    self.transact.order(created_at: :desc)
  end

  def transactions(query="", time=30.days)
    if query.nil? or query.empty?
      if time == 0
        self.transact.order(created_at: :desc)
      else
        self.transact
          .where("created_at > ?", Time.zone.now-time)
          .order(created_at: :desc)
      end
    else
      self.transact
        .where('CAST(amount AS varchar) LIKE :q OR ' \
               'description LIKE :q',
               q: "%#{query}%")
        .order(created_at: :desc)
    end
  end

  def credits(time=30.days)
    self.transact.where("credit = ? AND created_at > ?", true, Time.zone.now-time).sum(:amount)
  end

  def debits(time=30.days)
    self.transact.where("credit = ? AND created_at > ?", false, Time.zone.now-time).sum(:amount)
  end

  def avg_debit(time=30.days)
    self.transact.where("credit = ? AND created_at > ?", false, Time.zone.now-time).average(:amount).to_f
  end

  def max_debit(time=30.days)
    self.transact.where("credit = ? AND created_at > ?", false, Time.zone.now-time).maximum(:amount).to_f
  end

  def balance
    credits = self.transact.where(credit: true).sum(:amount)
    debits = self.transact.where(credit: false).sum(:amount)
    credits - debits
  end

end
