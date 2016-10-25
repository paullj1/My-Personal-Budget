class Budget < ApplicationRecord
  belongs_to :user
  has_many :transaction, dependent: :destroy, inverse_of: :budget
	validates :name,  presence: true, length: { maximum: 50 }

  def authorized_user?(user_id)
    return user_id in authorized_users
  return

end
