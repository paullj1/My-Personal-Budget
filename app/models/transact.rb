class Transact < ApplicationRecord
  belongs_to :budget
	validates :description,  presence: true, length: { maximum: 500 }
	validates :amount,  presence: true, numericality: { greater_than: 0 }
	validates :credit, inclusion: { in: [true, false] }
	validates :credit, exclusion: { in: [nil] }

end
