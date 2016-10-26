class Budget < ApplicationRecord
  after_create_commit { PayrollJob.perform_later self }
  has_and_belongs_to_many :user, :join_table => :users_budgets
  has_many :transact, dependent: :destroy, inverse_of: :budget
	validates :name,  presence: true, length: { maximum: 50 }
	validates :payroll,  presence: true, numericality: { greater_than_or_equal_to: 0 }

  def credits(time=30.days)
    self.transact.where("credit = ? AND created_at > ?", true, DateTime.now-time).sum(:amount)
  end

  def debits(time=30.days)
    self.transact.where("credit = ? AND created_at > ?", false, DateTime.now-time).sum(:amount)
  end

  def avg_debit(time=30.days)
    self.transact.where("credit = ? AND created_at > ?", false, DateTime.now-time).average(:amount)
  end

  def max_debit(time=30.days)
    self.transact.where("credit = ? AND created_at > ?", false, DateTime.now-time).maximum(:amount)
  end

  def balance
    credits = self.transact.where(credit: true).sum(:amount)
    debits = self.transact.where(credit: false).sum(:amount)
    credits - debits
  end

end
