package models

// User — пользователь системы.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	FullName string `json:"full_name"`
	Role     string `json:"role"`
}

// UserWithSecret — пользователь вместе с хэшем пароля.
// Используется только на сервере при входе.
type UserWithSecret struct {
	User
	PasswordHash string
}

// Course — учебный курс.
type Course struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Teacher     string `json:"teacher"`
	CreatedBy   int64  `json:"created_by"`
}

// CreateCourseInput — данные для создания курса.
type CreateCourseInput struct {
	Title       string
	Description string
	Teacher     string
}

type Assignment struct {
	ID          int64  `json:"id"`
	CourseID    int64  `json:"course_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	DueDate     int64  `json:"due_date"` // Unix timestamp
	CreatedBy   int64  `json:"created_by"`
}

type CreateAssignmentInput struct {
	CourseID    int64
	Title       string
	Description string
	DueDate     int64
}

type Submission struct {
	ID           int64   `json:"id"`
	AssignmentID int64   `json:"assignment_id"`
	UserID       int64   `json:"user_id"`
	Content      string  `json:"content"`
	Score        float64 `json:"score"` // -1 if ungraded
	Comment      string  `json:"comment"`
	SubmittedAt  int64   `json:"submitted_at"`
	GradedAt     int64   `json:"graded_at"`

	// Extra fields for UI convenience (joined data)
	UserName     string `json:"user_name,omitempty"`
	UserUsername string `json:"user_username,omitempty"`
	AssignTitle  string `json:"assign_title,omitempty"`
}

type SubmitInput struct {
	AssignmentID int64
	Content      string
}

type GradeInput struct {
	SubmissionID int64
	Score        float64
	Comment      string
}
